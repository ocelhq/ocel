package gcp

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const RootPostgres records.Root = "postgres"

const (
	cloudSQLEdition        = "ENTERPRISE"
	cloudSQLZonal          = "ZONAL"
	cloudSQLRegional       = "REGIONAL"
	cloudSQLDiskType       = "PD_SSD"
	cloudSQLDiskGB         = 10
	cloudSQLEncryptedOnly  = "ENCRYPTED_ONLY"
	cloudSQLRunnable       = "RUNNABLE"
	cloudSQLInstanceType   = "CLOUD_SQL_INSTANCE"
	cloudSQLPrimaryAddress = "PRIMARY"
	postgresPort           = 5432
	postgresAccount        = "ocel"
	postgresDatabaseName   = "ocel"
	postgresPasswordBytes  = 24
)

var cloudSQLVersions = map[string]string{
	"14": "POSTGRES_14",
	"15": "POSTGRES_15",
	"16": "POSTGRES_16",
	"17": "POSTGRES_17",
}

type cloudSQLSize struct {
	machineType         string
	availability        string
	pointInTimeRecovery bool
	deletionProtection  bool
	maxConnections      int
}

var (
	productionCloudSQL = cloudSQLSize{
		machineType:         "db-custom-1-3840",
		availability:        cloudSQLRegional,
		pointInTimeRecovery: true,
		deletionProtection:  true,
		maxConnections:      100,
	}
	previewCloudSQL = cloudSQLSize{
		machineType:    "db-f1-micro",
		availability:   cloudSQLZonal,
		maxConnections: 25,
	}
)

func (s cloudSQLSize) backups() *sqladmin.BackupConfiguration {
	return &sqladmin.BackupConfiguration{
		Enabled:                    true,
		PointInTimeRecoveryEnabled: s.pointInTimeRecovery,
		ForceSendFields:            []string{"PointInTimeRecoveryEnabled"},
	}
}

func (s cloudSQLSize) listDifferences(instance *sqladmin.DatabaseInstance) []string {
	settings := instance.Settings
	if settings == nil {
		settings = &sqladmin.Settings{}
	}
	backups := settings.BackupConfiguration
	if backups == nil {
		backups = &sqladmin.BackupConfiguration{}
	}
	var differences []string
	if settings.Tier != s.machineType {
		differences = append(differences, fmt.Sprintf("machine type changes from %s to %s", settings.Tier, s.machineType))
	}
	if settings.AvailabilityType != s.availability {
		differences = append(differences, fmt.Sprintf("availability changes from %s to %s", settings.AvailabilityType, s.availability))
	}
	if settings.DeletionProtectionEnabled != s.deletionProtection {
		differences = append(differences, fmt.Sprintf("deletion protection changes from %t to %t", settings.DeletionProtectionEnabled, s.deletionProtection))
	}
	if !backups.Enabled {
		differences = append(differences, "daily backups turn on")
	}
	if backups.PointInTimeRecoveryEnabled != s.pointInTimeRecovery {
		differences = append(differences, fmt.Sprintf("point-in-time recovery changes from %t to %t", backups.PointInTimeRecoveryEnabled, s.pointInTimeRecovery))
	}
	return differences
}

func cloudSQLSizeOf(tier environment.Tier) cloudSQLSize {
	if tier == environment.TierProduction {
		return productionCloudSQL
	}
	return previewCloudSQL
}

var settlingStates = []string{"PENDING_CREATE", "MAINTENANCE"}

var cloudSQLPatience = patience{attempts: 180, ceiling: 15 * time.Second}

type postgresSpec struct {
	version string
}

func readPostgres(resource provider.Resource) (postgresSpec, error) {
	declared := images.DefaultPostgresVersion
	if resource.Postgres != nil && resource.Postgres.Version != "" {
		declared = resource.Postgres.Version
	}
	version, known := cloudSQLVersions[declared]
	if !known {
		return postgresSpec{}, refusal.Refuse(refusal.CodeInvalid,
			"postgres %s asks for version %q, and Cloud SQL runs it here at %s", resource.Name, declared,
			strings.Join(slices.Sorted(maps.Keys(cloudSQLVersions)), ", "))
	}
	return postgresSpec{version: version}, nil
}

func (s postgresSpec) shapeProperties(region string, tier environment.Tier) map[string]any {
	size := cloudSQLSizeOf(tier)
	return map[string]any{
		"region":           region,
		"database_version": s.version,
		"settings": map[string]any{
			"tier":              size.machineType,
			"edition":           cloudSQLEdition,
			"availability_type": size.availability,
			"disk_type":         cloudSQLDiskType,
			"disk_size":         cloudSQLDiskGB,
		},
	}
}

type postgresInstances struct {
	clients *clients
	sql     *sqladmin.Service
	records records.Store
	cipher  seal.Cipher
	ref     provider.StackRef
}

func (p *Provider) openPostgres(ctx context.Context, ref provider.StackRef) (postgresInstances, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return postgresInstances{}, err
	}
	sql, err := clients.SQL()
	if err != nil {
		return postgresInstances{}, err
	}
	return postgresInstances{clients: clients, sql: sql, records: p.KeyValues(), cipher: p.Cipher(), ref: ref}, nil
}

func (p *Provider) ProvisionPostgres(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	spec, err := readPostgres(in.Resource)
	if err != nil {
		return provider.Binding{}, err
	}
	instances, err := p.openPostgres(ctx, in.Ref)
	if err != nil {
		return provider.Binding{}, err
	}
	return instances.provision(ctx, in.Resource, spec, progress)
}

func (p *Provider) PlanPostgres(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	spec, err := readPostgres(in.Resource)
	if err != nil {
		return "", "", err
	}
	instances, err := p.openPostgres(ctx, in.Ref)
	if err != nil {
		return "", "", err
	}
	return instances.plan(ctx, in.Resource.Name, spec)
}

func (i postgresInstances) plan(ctx context.Context, database string, spec postgresSpec) (provider.ChangeAction, string, error) {
	current, err := i.findInstance(ctx, database)
	if err != nil {
		return "", "", err
	}
	if current == nil {
		return provider.ActionCreate, "", nil
	}
	if !slices.Contains(settlingStates, current.State) {
		if err := refuseUnrunnable(database, current); err != nil {
			return "", "", err
		}
	}
	if err := refuseVersionChange(database, spec, current); err != nil {
		return "", "", err
	}
	differences := cloudSQLSizeOf(i.ref.Tier).listDifferences(current)
	held, err := i.hasDatabase(ctx, current.Name)
	if err != nil {
		return "", "", err
	}
	if !held {
		differences = append(differences, "the "+postgresDatabaseName+" database is created on Cloud SQL instance "+current.Name)
	}
	password, recorded, err := i.readRecordedPassword(ctx, database)
	if err != nil {
		return "", "", err
	}
	switch {
	case !recorded:
		differences = append(differences, "a new password is minted and set for the "+postgresAccount+" user")
	case password.SetOn != current.Name:
		differences = append(differences, "the recorded password is set for the "+postgresAccount+" user on Cloud SQL instance "+current.Name)
	}
	if len(differences) > 0 {
		return provider.ActionUpdate, strings.Join(differences, "; "), nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func refuseVersionChange(database string, spec postgresSpec, current *sqladmin.DatabaseInstance) error {
	if current.DatabaseVersion == spec.version {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"postgres %s asks for %s, and its Cloud SQL instance %s runs %s: a major version changes by an upgrade ocel does not run.\n"+
			"Declare %s again, or upgrade it with `gcloud sql instances patch %s --database-version=%s` and deploy again",
		database, spec.version, current.Name, current.DatabaseVersion, current.DatabaseVersion, current.Name, spec.version)
}

func (p *Provider) RemovePostgres(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	instances, err := p.openPostgres(ctx, ref)
	if err != nil {
		return err
	}
	return instances.remove(ctx, binding, progress)
}

func (i postgresInstances) labels(database string) map[string]string {
	return labelsFor(i.clients.Names, i.ref, postgresLabel, database)
}

func (i postgresInstances) filter(database string) string {
	labels := i.labels(database)
	terms := make([]string, 0, len(labels))
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		terms = append(terms, "settings.userLabels."+name+":"+labels[name])
	}
	return strings.Join(terms, " ")
}

func (i postgresInstances) findInstance(ctx context.Context, database string) (*sqladmin.DatabaseInstance, error) {
	listed, err := googleretry.Attempted(ctx, i.sql.Instances.List(i.clients.project).Filter(i.filter(database)).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("find the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	switch len(listed.Items) {
	case 0:
		return nil, nil
	case 1:
		return listed.Items[0], nil
	}
	named := make([]string, 0, len(listed.Items))
	for _, instance := range listed.Items {
		named = append(named, instance.Name)
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"postgres %s is labelled onto %d Cloud SQL instances, %s, and a database runs on one.\n"+
			"Delete the ones that hold nothing you need with `gcloud sql instances delete`, and deploy again",
		database, len(named), strings.Join(named, ", "))
}

func (i postgresInstances) provision(ctx context.Context, resource provider.Resource, spec postgresSpec, progress progress.Log) (provider.Binding, error) {
	current, err := i.findInstance(ctx, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	if current == nil {
		current, err = i.create(ctx, resource.Name, spec, progress)
	} else {
		current, err = i.awaitSettled(ctx, resource.Name, current)
	}
	if err != nil {
		return provider.Binding{}, err
	}
	if err := refuseVersionChange(resource.Name, spec, current); err != nil {
		return provider.Binding{}, err
	}
	if current, err = i.resize(ctx, resource.Name, current, progress); err != nil {
		return provider.Binding{}, err
	}
	if current, err = i.awaitAddress(ctx, resource.Name, current); err != nil {
		return provider.Binding{}, err
	}
	if err := i.ensureDatabase(ctx, current.Name); err != nil {
		return provider.Binding{}, err
	}
	password, err := i.ensurePassword(ctx, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	if password.SetOn != current.Name {
		if err := i.setUserPassword(ctx, current.Name, password.plain); err != nil {
			return provider.Binding{}, err
		}
		if err := i.recordPasswordSet(ctx, resource.Name, current.Name); err != nil {
			return provider.Binding{}, err
		}
	}
	return provider.Binding{
		Type:     provider.BindingPostgres,
		Name:     resource.Name,
		Resource: resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     i.addressOf(current),
			provider.PropertyPort:     strconv.Itoa(postgresPort),
			provider.PropertyDatabase: postgresDatabaseName,
			provider.PropertyUsername: postgresAccount,
			provider.PropertyPassword: password.plain,
			provider.PropertyTLSMode:  bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE.String(),
		},
	}, nil
}

func (i postgresInstances) desiredInstance(name string, spec postgresSpec, database string) *sqladmin.DatabaseInstance {
	project, grows, size := i.clients.project, true, cloudSQLSizeOf(i.ref.Tier)
	return &sqladmin.DatabaseInstance{
		Name:            name,
		Region:          i.clients.region,
		DatabaseVersion: spec.version,
		InstanceType:    cloudSQLInstanceType,
		Settings: &sqladmin.Settings{
			Edition:             cloudSQLEdition,
			Tier:                size.machineType,
			AvailabilityType:    size.availability,
			DataDiskType:        cloudSQLDiskType,
			DataDiskSizeGb:      cloudSQLDiskGB,
			StorageAutoResize:   &grows,
			UserLabels:          i.labels(database),
			BackupConfiguration: size.backups(),
			IpConfiguration: &sqladmin.IpConfiguration{
				Ipv4Enabled: false,
				SslMode:     cloudSQLEncryptedOnly,
				PscConfig: &sqladmin.PscConfig{
					PscEnabled:              true,
					AllowedConsumerProjects: []string{project},
					PscAutoConnections: []*sqladmin.PscAutoConnectionConfig{
						{ConsumerNetwork: i.clients.NetworkPath(i.ref.Tier), ConsumerProject: project},
					},
				},
				ForceSendFields: []string{"Ipv4Enabled"},
			},
			DeletionProtectionEnabled: size.deletionProtection,
			ForceSendFields:           []string{"DeletionProtectionEnabled"},
		},
	}
}

func (i postgresInstances) create(ctx context.Context, database string, spec postgresSpec, progress progress.Log) (*sqladmin.DatabaseInstance, error) {
	suffix := make([]byte, instanceSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("mint the suffix a new Cloud SQL instance is named with: %w", err)
	}
	name := i.clients.PostgresInstance(i.ref.Project, i.ref.Name.Env, database) + "-" + hex.EncodeToString(suffix)
	ensureProgress(progress).Say("Creating postgres " + database + " as Cloud SQL instance " + name + " in " + i.clients.region +
		": a new instance takes several minutes")
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Insert(i.clients.project, i.desiredInstance(name, spec, database)).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("create the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	if err := i.awaited(ctx, "creating postgres "+database, started); err != nil {
		return nil, err
	}
	return i.readInstance(ctx, database, name)
}

func (i postgresInstances) resize(ctx context.Context, database string, current *sqladmin.DatabaseInstance, progress progress.Log) (*sqladmin.DatabaseInstance, error) {
	size := cloudSQLSizeOf(i.ref.Tier)
	differences := size.listDifferences(current)
	if len(differences) == 0 {
		return current, nil
	}
	ensureProgress(progress).Say("Resizing postgres " + database + "'s Cloud SQL instance " + current.Name + " as the " + string(i.ref.Tier) + " tier runs it (" +
		strings.Join(differences, "; ") + "): Cloud SQL restarts an instance whose machine type or availability changes")
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Patch(i.clients.project, current.Name, &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{
			Tier:                      size.machineType,
			AvailabilityType:          size.availability,
			BackupConfiguration:       size.backups(),
			DeletionProtectionEnabled: size.deletionProtection,
			ForceSendFields:           []string{"DeletionProtectionEnabled"},
		},
	}).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("resize the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	if err := i.awaited(ctx, "resizing postgres "+database, started); err != nil {
		return nil, err
	}
	return i.readInstance(ctx, database, current.Name)
}

func (i postgresInstances) readInstance(ctx context.Context, database, name string) (*sqladmin.DatabaseInstance, error) {
	instance, err := googleretry.Attempted(ctx, i.sql.Instances.Get(i.clients.project, name).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	return instance, nil
}

func (i postgresInstances) awaitSettled(ctx context.Context, database string, current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, error) {
	if slices.Contains(settlingStates, current.State) {
		var err error
		current, err = waiting(ctx, cloudSQLPatience, "postgres "+database+" to finish "+strings.ToLower(current.State),
			func() (*sqladmin.DatabaseInstance, error) { return i.readInstance(ctx, database, current.Name) },
			func(instance *sqladmin.DatabaseInstance) bool {
				return !slices.Contains(settlingStates, instance.State)
			})
		if err != nil {
			return nil, err
		}
	}
	if err := refuseUnrunnable(database, current); err != nil {
		return nil, err
	}
	return current, nil
}

func refuseUnrunnable(database string, current *sqladmin.DatabaseInstance) error {
	if current.State == cloudSQLRunnable {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"postgres %s runs on the Cloud SQL instance %s, which is %s, and a database is bound only once it is %s.\n"+
			"Start it with `gcloud sql instances patch %s --activation-policy=ALWAYS` if it was stopped, and deploy again",
		database, current.Name, current.State, cloudSQLRunnable, current.Name)
}

func addressOf(instance *sqladmin.DatabaseInstance, network string, emulated bool) string {
	if instance.Settings != nil && instance.Settings.IpConfiguration != nil && instance.Settings.IpConfiguration.PscConfig != nil {
		for _, auto := range instance.Settings.IpConfiguration.PscConfig.PscAutoConnections {
			if auto.ConsumerNetwork == network && auto.IpAddress != "" {
				return auto.IpAddress
			}
		}
	}
	if emulated {
		for _, mapping := range instance.IpAddresses {
			if mapping.Type == cloudSQLPrimaryAddress && mapping.IpAddress != "" {
				return mapping.IpAddress
			}
		}
	}
	return ""
}

func (i postgresInstances) addressOf(instance *sqladmin.DatabaseInstance) string {
	return addressOf(instance, i.clients.NetworkPath(i.ref.Tier), i.clients.emulated())
}

func (i postgresInstances) awaitAddress(ctx context.Context, database string, current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, error) {
	if i.addressOf(current) != "" {
		return current, nil
	}
	reached, err := until(ctx, "postgres "+database+" to be given an address on "+i.clients.Network(i.ref.Tier),
		func() (*sqladmin.DatabaseInstance, error) { return i.readInstance(ctx, database, current.Name) },
		func(instance *sqladmin.DatabaseInstance) bool { return i.addressOf(instance) != "" })
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the Cloud SQL instance postgres %s runs on, %s, has no address on the %s network yet, so nothing names where an app reaches it: %v\n"+
				"Check that the tier's private network has its Cloud SQL connection policy, then deploy again",
			database, current.Name, i.clients.Network(i.ref.Tier), err)
	}
	return reached, nil
}

func (i postgresInstances) awaited(ctx context.Context, doing string, started *sqladmin.Operation) error {
	_, err := awaitOperation(ctx, cloudSQLPatience, "Cloud SQL", doing, started, cloudSQLOutcome,
		func(name string) (*sqladmin.Operation, error) {
			return googleretry.Attempted(ctx, i.sql.Operations.Get(i.clients.project, name).Context(ctx).Do)
		})
	return err
}

func cloudSQLOutcome(operation *sqladmin.Operation) operationOutcome {
	outcome := operationOutcome{name: operation.Name, done: operation.Status == operationDone}
	if operation.Error != nil && len(operation.Error.Errors) > 0 {
		failed := operation.Error.Errors[0]
		outcome.failure = cmp.Or(failed.Message, failed.Code)
	}
	return outcome
}

func (i postgresInstances) hasDatabase(ctx context.Context, instance string) (bool, error) {
	_, err := googleretry.Attempted(ctx, i.sql.Databases.Get(i.clients.project, instance, postgresDatabaseName).Context(ctx).Do)
	switch {
	case err == nil:
		return true, nil
	case absent(err):
		return false, nil
	}
	return false, fmt.Errorf("read the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
}

func (i postgresInstances) ensureDatabase(ctx context.Context, instance string) error {
	held, err := i.hasDatabase(ctx, instance)
	if err != nil || held {
		return err
	}
	started, err := googleretry.Attempted(ctx, i.sql.Databases.Insert(i.clients.project, instance, &sqladmin.Database{Name: postgresDatabaseName}).Context(ctx).Do)
	if taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
	}
	return i.awaited(ctx, "creating the "+postgresDatabaseName+" database on "+instance, started)
}

func (i postgresInstances) setUserPassword(ctx context.Context, instance, password string) error {
	_, err := googleretry.Attempted(ctx, i.sql.Users.Get(i.clients.project, instance, postgresAccount).Context(ctx).Do)
	switch {
	case absent(err):
		started, err := googleretry.Attempted(ctx, i.sql.Users.Insert(i.clients.project, instance, &sqladmin.User{Name: postgresAccount, Password: password}).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("create the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
		}
		return i.awaited(ctx, "creating the "+postgresAccount+" user on "+instance, started)
	case err != nil:
		return fmt.Errorf("read the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
	}
	started, err := googleretry.Attempted(ctx, i.sql.Users.Update(i.clients.project, instance, &sqladmin.User{Password: password}).Name(postgresAccount).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("set the %s user's password on Cloud SQL instance %s: %w", postgresAccount, instance, err)
	}
	return i.awaited(ctx, "setting the "+postgresAccount+" user's password on "+instance, started)
}

type recordedPassword struct {
	Sealed []byte `json:"sealed"`
	SetOn  string `json:"setOn,omitempty"`
	plain  string
}

func (i postgresInstances) passwordKey(database string) records.Key {
	return records.Partition{Tier: i.ref.Tier, Root: RootPostgres}.Key(i.ref.Project, i.ref.Name.Env, database)
}

func (i postgresInstances) passwordBound(database string) seal.AssociatedData {
	return seal.BindToProject(i.ref.Project, "postgres password", string(i.ref.Tier), i.ref.Name.Env, database)
}

func (i postgresInstances) ensurePassword(ctx context.Context, database string) (recordedPassword, error) {
	key := i.passwordKey(database)
	err := records.Change(ctx, i.records, key, func(recorded records.Entry) ([]byte, bool, error) {
		if len(recorded.Value) > 0 {
			return nil, false, nil
		}
		raw := make([]byte, postgresPasswordBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, false, fmt.Errorf("mint the password postgres %s is reached with: %w", database, err)
		}
		sealed, err := i.cipher.Seal(ctx, i.ref.Tier, i.passwordBound(database), []byte(hex.EncodeToString(raw)))
		if err != nil {
			return nil, false, err
		}
		value, err := json.Marshal(recordedPassword{Sealed: sealed})
		return value, err == nil, err
	})
	if err != nil {
		return recordedPassword{}, err
	}
	recorded, found, err := i.readRecordedPassword(ctx, database)
	if err != nil {
		return recordedPassword{}, err
	}
	if !found {
		return recordedPassword{}, fmt.Errorf("read the password recorded for postgres %s: %w", database, records.ErrNotFound)
	}
	opened, err := i.cipher.Open(ctx, i.ref.Tier, i.passwordBound(database), recorded.Sealed)
	if err != nil {
		return recordedPassword{}, fmt.Errorf("open the password recorded for postgres %s: %w", database, err)
	}
	recorded.plain = string(opened)
	return recorded, nil
}

func (i postgresInstances) readRecordedPassword(ctx context.Context, database string) (recordedPassword, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, i.records, i.passwordKey(database))
	if err != nil {
		return recordedPassword{}, false, fmt.Errorf("read the password recorded for postgres %s: %w", database, err)
	}
	if len(entry.Value) == 0 {
		return recordedPassword{}, false, nil
	}
	var recorded recordedPassword
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return recordedPassword{}, false, fmt.Errorf("read the password recorded for postgres %s: %w", database, err)
	}
	return recorded, true, nil
}

func (i postgresInstances) recordPasswordSet(ctx context.Context, database, instance string) error {
	return records.Change(ctx, i.records, i.passwordKey(database), func(entry records.Entry) ([]byte, bool, error) {
		var recorded recordedPassword
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return nil, false, fmt.Errorf("read the password recorded for postgres %s: %w", database, err)
		}
		recorded.SetOn = instance
		value, err := json.Marshal(recorded)
		return value, err == nil, err
	})
}

func (i postgresInstances) remove(ctx context.Context, binding provider.Binding, progress progress.Log) error {
	current, err := i.findInstance(ctx, binding.Name)
	if err != nil {
		return err
	}
	if current != nil {
		if err := i.unprotect(ctx, binding.Name, current); err != nil {
			return err
		}
		ensureProgress(progress).Say("Removing postgres " + binding.Name + ", its Cloud SQL instance " + current.Name + " and its data")
		started, err := googleretry.Attempted(ctx, i.sql.Instances.Delete(i.clients.project, current.Name).Context(ctx).Do)
		switch {
		case absent(err):
		case err != nil:
			return fmt.Errorf("delete the Cloud SQL instance postgres %s runs on: %w", binding.Name, err)
		default:
			if err := i.awaited(ctx, "deleting postgres "+binding.Name, started); err != nil {
				return err
			}
		}
	}
	return records.Forget(ctx, i.records, i.passwordKey(binding.Name))
}

func (i postgresInstances) unprotect(ctx context.Context, database string, current *sqladmin.DatabaseInstance) error {
	if current.Settings == nil || !current.Settings.DeletionProtectionEnabled {
		return nil
	}
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Patch(i.clients.project, current.Name, &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{DeletionProtectionEnabled: false, ForceSendFields: []string{"DeletionProtectionEnabled"}},
	}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("turn off deletion protection on the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	return i.awaited(ctx, "turning off deletion protection on postgres "+database, started)
}
