package postgres

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
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
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
	Port                   = 5432
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

func cloudSQLSizeOf(tier environment.Tier) cloudSQLSize {
	if tier == environment.TierProduction {
		return productionCloudSQL
	}
	return previewCloudSQL
}

var settlingStates = []string{"PENDING_CREATE", "MAINTENANCE"}

var cloudSQLPatience = gcloud.Patience{Attempts: 180, Ceiling: 15 * time.Second}

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

func pricedProperties(region string, instance *sqladmin.DatabaseInstance) map[string]any {
	settings := settingsOf(instance)
	return map[string]any{
		"region":           region,
		"database_version": instance.DatabaseVersion,
		"settings": map[string]any{
			"tier":              settings.Tier,
			"edition":           settings.Edition,
			"availability_type": settings.AvailabilityType,
			"disk_type":         settings.DataDiskType,
			"disk_size":         settings.DataDiskSizeGb,
		},
	}
}

type postgresInstances struct {
	clients    *gcloud.Clients
	sql        *sqladmin.Service
	records    records.Store
	cipher     seal.Cipher
	ref        provider.StackRef
	skipChecks bool
}

type declaredInstance struct {
	database string
	patched  surface.RecordedPatch[sqladmin.DatabaseInstance, sqladmin.DatabaseInstance]
}

func (i postgresInstances) declare(ctx context.Context, database string, spec postgresSpec) (declaredInstance, error) {
	labels, err := i.labels(database)
	if err != nil {
		return declaredInstance{}, err
	}
	evaluated, _ := surface.ReadEvaluated(ctx, i.ref)
	result := evaluated.For(transformKind, database)
	record := surface.Record{Store: i.records, Key: surface.KeyFor(i.ref, transformKind, database)}
	own := newInstance(spec, i.ref.Tier, i.clients.Region, i.clients.Project(), i.clients.NetworkPath(i.ref.Tier), labels)
	own.Tags = i.clients.InlineTierTags(i.ref.Tier)
	patched, err := instanceSurface.PatchRecorded(ctx, record, "postgres "+database, *own, result)
	if err != nil {
		return declaredInstance{}, err
	}
	return declaredInstance{database: database, patched: patched}, refuseFewerConnections(database, i.ref.Tier, &patched.Desired)
}

func newInstance(spec postgresSpec, tier environment.Tier, region, project, network string, labels map[string]string) *sqladmin.DatabaseInstance {
	grows, size := true, cloudSQLSizeOf(tier)
	return &sqladmin.DatabaseInstance{
		Region:          region,
		DatabaseVersion: spec.version,
		InstanceType:    cloudSQLInstanceType,
		Settings: &sqladmin.Settings{
			Edition:             cloudSQLEdition,
			Tier:                size.machineType,
			AvailabilityType:    size.availability,
			DataDiskType:        cloudSQLDiskType,
			DataDiskSizeGb:      cloudSQLDiskGB,
			StorageAutoResize:   &grows,
			UserLabels:          labels,
			BackupConfiguration: size.backups(),
			IpConfiguration: &sqladmin.IpConfiguration{
				Ipv4Enabled: false,
				SslMode:     cloudSQLEncryptedOnly,
				PscConfig: &sqladmin.PscConfig{
					PscEnabled:              true,
					AllowedConsumerProjects: []string{project},
					PscAutoConnections: []*sqladmin.PscAutoConnectionConfig{
						{ConsumerNetwork: network, ConsumerProject: project},
					},
				},
				ForceSendFields: []string{"Ipv4Enabled"},
			},
			DeletionProtectionEnabled: size.deletionProtection,
			ForceSendFields:           []string{"DeletionProtectionEnabled"},
		},
	}
}

func (d declaredInstance) network() string { return placedNetworkOf(&d.patched.Desired) }

func (d declaredInstance) drift(current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, []string, error) {
	if err := d.refuseDiskTypeChange(current); err != nil {
		return nil, nil, err
	}
	held, wanted := settingsOf(current), d.patched.Desired.Settings
	update := &sqladmin.DatabaseInstance{Settings: &sqladmin.Settings{}}
	var differences []string
	if held.Tier != wanted.Tier {
		update.Settings.Tier = wanted.Tier
		differences = append(differences, fmt.Sprintf("machine type changes from %s to %s", held.Tier, wanted.Tier))
	}
	if held.AvailabilityType != wanted.AvailabilityType {
		update.Settings.AvailabilityType = wanted.AvailabilityType
		differences = append(differences, fmt.Sprintf("availability changes from %s to %s", held.AvailabilityType, wanted.AvailabilityType))
	}
	if held.DeletionProtectionEnabled != wanted.DeletionProtectionEnabled {
		update.Settings.DeletionProtectionEnabled = wanted.DeletionProtectionEnabled
		update.Settings.ForceSendFields = append(update.Settings.ForceSendFields, "DeletionProtectionEnabled")
		differences = append(differences, fmt.Sprintf("deletion protection changes from %t to %t", held.DeletionProtectionEnabled, wanted.DeletionProtectionEnabled))
	}
	heldBackups, wantedBackups := backupsOf(current), wanted.BackupConfiguration
	if heldBackups.Enabled != wantedBackups.Enabled {
		differences = append(differences, "daily backups turn "+surface.ShowOnOff(wantedBackups.Enabled))
	}
	if heldBackups.PointInTimeRecoveryEnabled != wantedBackups.PointInTimeRecoveryEnabled {
		differences = append(differences, fmt.Sprintf("point-in-time recovery changes from %t to %t", heldBackups.PointInTimeRecoveryEnabled, wantedBackups.PointInTimeRecoveryEnabled))
	}
	if heldBackups.Enabled != wantedBackups.Enabled || heldBackups.PointInTimeRecoveryEnabled != wantedBackups.PointInTimeRecoveryEnabled {
		backupsUpdate(update, &d.patched.Desired).PointInTimeRecoveryEnabled = wantedBackups.PointInTimeRecoveryEnabled
		update.Settings.BackupConfiguration.ForceSendFields = append(update.Settings.BackupConfiguration.ForceSendFields, "PointInTimeRecoveryEnabled")
	}
	patched, err := d.patched.Drift(current, update)
	if err != nil {
		return nil, nil, err
	}
	return update, append(differences, patched...), nil
}

func (d declaredInstance) refuseDiskTypeChange(current *sqladmin.DatabaseInstance) error {
	held, wanted := settingsOf(current).DataDiskType, d.patched.Desired.Settings.DataDiskType
	if held == "" || held == wanted {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform sets postgres %s's %s.%s to %s, and its Cloud SQL instance %s keeps the %s disk it was made with: patch it back to %s",
		d.database, transformSurface, diskTypePath, wanted, current.Name, held, held)
}

func (d declaredInstance) refuseEndpointMove(current *sqladmin.DatabaseInstance) error {
	return gcloud.RefuseEndpointMove("postgres "+d.database, "Cloud SQL instance "+current.Name, gcloud.DatabaseNetworks(current), d.network())
}

func (i postgresInstances) refusePlacedNetworkAbsent(ctx context.Context, declared declaredInstance) error {
	if i.skipChecks || !declared.patched.Sets(surface.DatabasePlacement) {
		return nil
	}
	return gcloud.RefuseNetworkAbsent(ctx, i.clients, "postgres "+declared.database, declared.network())
}

func (p *Postgres) openPostgres(ctx context.Context, ref provider.StackRef) (postgresInstances, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return postgresInstances{}, err
	}
	sql, err := clients.SQL()
	if err != nil {
		return postgresInstances{}, err
	}
	return postgresInstances{clients: clients, sql: sql, records: p.records, cipher: p.cipher, ref: ref, skipChecks: p.skipChecks}, nil
}

func (p *Postgres) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
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

func (p *Postgres) Plan(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
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
	declared, err := i.declare(ctx, database, spec)
	if err != nil {
		return "", "", err
	}
	current, err := i.findInstance(ctx, database)
	if err != nil {
		return "", "", err
	}
	if current == nil {
		return provider.ActionCreate, "", i.refusePlacedNetworkAbsent(ctx, declared)
	}
	if !slices.Contains(settlingStates, current.State) {
		if err := refuseUnrunnable(database, current); err != nil {
			return "", "", err
		}
	}
	if err := refuseVersionChange(database, spec, current); err != nil {
		return "", "", err
	}
	if err := declared.refuseEndpointMove(current); err != nil {
		return "", "", err
	}
	_, differences, err := declared.drift(current)
	if err != nil {
		return "", "", err
	}
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

func (p *Postgres) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	instances, err := p.openPostgres(ctx, ref)
	if err != nil {
		return err
	}
	return instances.remove(ctx, binding, progress)
}

func (i postgresInstances) labels(database string) (map[string]string, error) {
	return names.LabelsFor(i.clients.Names, i.ref, names.PostgresLabel, database)
}

func (i postgresInstances) filter(database string) (string, error) {
	labels, err := i.labels(database)
	if err != nil {
		return "", err
	}
	terms := make([]string, 0, len(labels))
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		terms = append(terms, "settings.userLabels."+name+":"+labels[name])
	}
	return strings.Join(terms, " "), nil
}

func (i postgresInstances) findInstance(ctx context.Context, database string) (*sqladmin.DatabaseInstance, error) {
	filter, err := i.filter(database)
	if err != nil {
		return nil, err
	}
	listed, err := googleretry.Attempted(ctx, i.sql.Instances.List(i.clients.Project()).Filter(filter).Context(ctx).Do)
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
	declared, err := i.declare(ctx, resource.Name, spec)
	if err != nil {
		return provider.Binding{}, err
	}
	var current *sqladmin.DatabaseInstance
	err = declared.patched.Apply(ctx, func() error {
		if current, err = i.ensureInstance(ctx, declared, spec, progress); err != nil {
			return err
		}
		current, err = i.update(ctx, declared, current, progress)
		return err
	})
	if err != nil {
		return provider.Binding{}, err
	}
	if current, err = i.awaitAddress(ctx, declared, current); err != nil {
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
			provider.PropertyHost:     i.addressOf(current, declared.network()),
			provider.PropertyPort:     strconv.Itoa(Port),
			provider.PropertyDatabase: postgresDatabaseName,
			provider.PropertyUsername: postgresAccount,
			provider.PropertyPassword: password.plain,
			provider.PropertyTLSMode:  bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE.String(),
		},
	}, nil
}

func (i postgresInstances) ensureInstance(ctx context.Context, declared declaredInstance, spec postgresSpec, progress progress.Log) (*sqladmin.DatabaseInstance, error) {
	current, err := i.findInstance(ctx, declared.database)
	if err != nil {
		return nil, err
	}
	if current == nil {
		if err := i.refusePlacedNetworkAbsent(ctx, declared); err != nil {
			return nil, err
		}
		return i.create(ctx, declared, progress)
	}
	if current, err = i.awaitSettled(ctx, declared.database, current); err != nil {
		return nil, err
	}
	if err := refuseVersionChange(declared.database, spec, current); err != nil {
		return nil, err
	}
	if err := declared.refuseEndpointMove(current); err != nil {
		return nil, err
	}
	return current, i.clients.EnsureTierTag(ctx, i.ref.Tier, i.clients.SQLInstanceTagged(current.Name))
}

func (i postgresInstances) create(ctx context.Context, declared declaredInstance, progress progress.Log) (*sqladmin.DatabaseInstance, error) {
	name, err := i.clients.PostgresInstance(i.ref.Project, i.ref.Name.Env, declared.database, naming.NewRandomTag())
	if err != nil {
		return nil, err
	}
	gcloud.EnsureProgress(progress).Say("Creating postgres " + declared.database + " as Cloud SQL instance " + name + " in " + i.clients.Region +
		": a new instance takes several minutes")
	desired := declared.patched.Desired
	desired.Name = name
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Insert(i.clients.Project(), &desired).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("create the Cloud SQL instance postgres %s runs on: %w", declared.database, err)
	}
	if err := i.awaited(ctx, "creating postgres "+declared.database, started); err != nil {
		return nil, err
	}
	return i.readInstance(ctx, declared.database, name)
}

func (i postgresInstances) update(ctx context.Context, declared declaredInstance, current *sqladmin.DatabaseInstance, progress progress.Log) (*sqladmin.DatabaseInstance, error) {
	update, differences, err := declared.drift(current)
	if err != nil || len(differences) == 0 {
		return current, err
	}
	database := declared.database
	gcloud.EnsureProgress(progress).Say("Updating postgres " + database + "'s Cloud SQL instance " + current.Name + " (" +
		strings.Join(differences, "; ") + "): Cloud SQL restarts an instance whose machine type or availability changes")
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Patch(i.clients.Project(), current.Name, update).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("update the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	if err := i.awaited(ctx, "updating postgres "+database, started); err != nil {
		return nil, err
	}
	return i.readInstance(ctx, database, current.Name)
}

func (i postgresInstances) readInstance(ctx context.Context, database, name string) (*sqladmin.DatabaseInstance, error) {
	instance, err := googleretry.Attempted(ctx, i.sql.Instances.Get(i.clients.Project(), name).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	return instance, nil
}

func (i postgresInstances) awaitSettled(ctx context.Context, database string, current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, error) {
	if slices.Contains(settlingStates, current.State) {
		var err error
		current, err = gcloud.Waiting(ctx, cloudSQLPatience, "postgres "+database+" to finish "+strings.ToLower(current.State),
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

func (i postgresInstances) addressOf(instance *sqladmin.DatabaseInstance, network string) string {
	return addressOf(instance, network, i.clients.Emulated())
}

func (i postgresInstances) awaitAddress(ctx context.Context, declared declaredInstance, current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, error) {
	network, database := declared.network(), declared.database
	if i.addressOf(current, network) != "" {
		return current, nil
	}
	reached, err := gcloud.Until(ctx, "postgres "+database+" to be given an address on "+networkName(network),
		func() (*sqladmin.DatabaseInstance, error) { return i.readInstance(ctx, database, current.Name) },
		func(instance *sqladmin.DatabaseInstance) bool { return i.addressOf(instance, network) != "" })
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the Cloud SQL instance postgres %s runs on, %s, has no address on the %s network yet, so nothing names where an app reaches it: %v\n"+
				"Check that the %s network has a Cloud SQL service connection policy in %s, then deploy again",
			database, current.Name, networkName(network), err, networkName(network), i.clients.Region)
	}
	return reached, nil
}

func (i postgresInstances) awaited(ctx context.Context, doing string, started *sqladmin.Operation) error {
	_, err := gcloud.AwaitOperation(ctx, cloudSQLPatience, "Cloud SQL", doing, started, cloudSQLOutcome,
		func(name string) (*sqladmin.Operation, error) {
			return googleretry.Attempted(ctx, i.sql.Operations.Get(i.clients.Project(), name).Context(ctx).Do)
		})
	return err
}

func cloudSQLOutcome(operation *sqladmin.Operation) gcloud.OperationOutcome {
	outcome := gcloud.OperationOutcome{Name: operation.Name, Done: operation.Status == gcloud.OperationDone}
	if operation.Error != nil && len(operation.Error.Errors) > 0 {
		failed := operation.Error.Errors[0]
		outcome.Failure = cmp.Or(failed.Message, failed.Code)
	}
	return outcome
}

func (i postgresInstances) hasDatabase(ctx context.Context, instance string) (bool, error) {
	_, err := googleretry.Attempted(ctx, i.sql.Databases.Get(i.clients.Project(), instance, postgresDatabaseName).Context(ctx).Do)
	switch {
	case err == nil:
		return true, nil
	case gcloud.Absent(err):
		return false, nil
	}
	return false, fmt.Errorf("read the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
}

func (i postgresInstances) ensureDatabase(ctx context.Context, instance string) error {
	held, err := i.hasDatabase(ctx, instance)
	if err != nil || held {
		return err
	}
	started, err := googleretry.Attempted(ctx, i.sql.Databases.Insert(i.clients.Project(), instance, &sqladmin.Database{Name: postgresDatabaseName}).Context(ctx).Do)
	if gcloud.Taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
	}
	return i.awaited(ctx, "creating the "+postgresDatabaseName+" database on "+instance, started)
}

func (i postgresInstances) setUserPassword(ctx context.Context, instance, password string) error {
	_, err := googleretry.Attempted(ctx, i.sql.Users.Get(i.clients.Project(), instance, postgresAccount).Context(ctx).Do)
	switch {
	case gcloud.Absent(err):
		started, err := googleretry.Attempted(ctx, i.sql.Users.Insert(i.clients.Project(), instance, &sqladmin.User{Name: postgresAccount, Password: password}).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("create the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
		}
		return i.awaited(ctx, "creating the "+postgresAccount+" user on "+instance, started)
	case err != nil:
		return fmt.Errorf("read the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
	}
	started, err := googleretry.Attempted(ctx, i.sql.Users.Update(i.clients.Project(), instance, &sqladmin.User{Password: password}).Name(postgresAccount).Context(ctx).Do)
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
		if err := i.clients.RemoveTierTag(ctx, i.ref.Tier, i.clients.SQLInstanceTagged(current.Name)); err != nil {
			return err
		}
		gcloud.EnsureProgress(progress).Say("Removing postgres " + binding.Name + ", its Cloud SQL instance " + current.Name + " and its data")
		started, err := googleretry.Attempted(ctx, i.sql.Instances.Delete(i.clients.Project(), current.Name).Context(ctx).Do)
		switch {
		case gcloud.Absent(err):
		case err != nil:
			return fmt.Errorf("delete the Cloud SQL instance postgres %s runs on: %w", binding.Name, err)
		default:
			if err := i.awaited(ctx, "deleting postgres "+binding.Name, started); err != nil {
				return err
			}
		}
	}
	patches := surface.Record{Store: i.records, Key: surface.KeyFor(i.ref, transformKind, binding.Name)}
	if err := patches.Forget(ctx); err != nil {
		return err
	}
	return records.Forget(ctx, i.records, i.passwordKey(binding.Name))
}

func (i postgresInstances) unprotect(ctx context.Context, database string, current *sqladmin.DatabaseInstance) error {
	if current.Settings == nil || !current.Settings.DeletionProtectionEnabled {
		return nil
	}
	started, err := googleretry.Attempted(ctx, i.sql.Instances.Patch(i.clients.Project(), current.Name, &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{DeletionProtectionEnabled: false, ForceSendFields: []string{"DeletionProtectionEnabled"}},
	}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("turn off deletion protection on the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	return i.awaited(ctx, "turning off deletion protection on postgres "+database, started)
}

type Postgres struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
	records     records.Store
	cipher      seal.Cipher
	skipChecks  bool
}

func New(openClients func(ctx context.Context) (*gcloud.Clients, error), store records.Store, cipher seal.Cipher, skipChecks bool) *Postgres {
	return &Postgres{openClients: openClients, records: store, cipher: cipher, skipChecks: skipChecks}
}

var bindingTypes = []provider.BindingType{provider.BindingPostgres}

func (p *Postgres) Listing() component.Component {
	return component.Component{Types: bindingTypes, ListResources: p.ListResources}
}

func (p *Postgres) Component() component.Component {
	return component.Component{
		Types:         bindingTypes,
		Hooks:         resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		ListResources: p.ListResources,
		Egress:        true,
		Parts:         []component.Part{network.Part, component.NewPart(bootstrap.Keys)},
		Preflight: func(resource provider.Resource) error {
			_, err := readPostgres(resource)
			return err
		},
		Inventory:  inventoryPostgres,
		Transforms: map[provider.BindingType]*component.Transform{provider.BindingPostgres: transformTarget()},
	}
}

func inventoryPostgres(inventory component.Inventory, declared []provider.Resource) error {
	tier := inventory.Request.Deploy.Tier
	for _, resource := range declared {
		spec, err := readPostgres(resource)
		if err != nil {
			return err
		}
		instance, err := inventory.Names.UntaggedPostgresInstance(inventory.Request.Deploy.Slug, inventory.Request.Deploy.Env, resource.Name)
		if err != nil {
			return err
		}
		result := inventory.Transforms.For(transformKind, resource.Name)
		known, unknown := instanceSurface.SplitOutputs(result.Patches[transformSurface])
		own := newInstance(spec, tier, inventory.Region, inventory.Names.Project(), inventory.Names.NetworkPath(tier), nil)
		patched, err := instanceSurface.Patch("postgres "+resource.Name, *own, known, nil, surface.Recorded{})
		if err != nil {
			return err
		}
		inventory.Tree.Add(inventory.Environment, inventory.Vendor, cost.SQLInstanceType, instance, inventory.Region, pricedProperties(inventory.Region, &patched.Desired), surface.ListPriced(unknown, pricedSettings)...)
	}
	return nil
}

var pricedSettings = map[string]string{
	"settings.tier":             "settings.tier",
	"settings.edition":          "settings.edition",
	"settings.availabilityType": "settings.availability_type",
	"settings.diskType":         "settings.disk_type",
	"settings.diskSize":         "settings.disk_size",
}
