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

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	cloudSQLEdition        = "ENTERPRISE"
	cloudSQLTier           = "db-f1-micro"
	cloudSQLZonal          = "ZONAL"
	cloudSQLDiskType       = "PD_SSD"
	cloudSQLDiskGB         = 10
	cloudSQLEncryptedOnly  = "ENCRYPTED_ONLY"
	cloudSQLRunnable       = "RUNNABLE"
	cloudSQLInstanceType   = "CLOUD_SQL_INSTANCE"
	postgresPort           = 5432
	postgresAccount        = "ocel"
	postgresDatabaseName   = "ocel"
	postgresPasswordBytes  = 24
	postgresSecretName     = "password"
	recordedPasswordFolder = "postgres"
)

var cloudSQLVersions = map[string]string{
	"14": "POSTGRES_14",
	"15": "POSTGRES_15",
	"16": "POSTGRES_16",
	"17": "POSTGRES_17",
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

func (s postgresSpec) shapeProperties(region string) map[string]any {
	return map[string]any{
		"region":           region,
		"database_version": s.version,
		"settings": map[string]any{
			"tier":              cloudSQLTier,
			"edition":           cloudSQLEdition,
			"availability_type": cloudSQLZonal,
			"disk_type":         cloudSQLDiskType,
			"disk_size":         cloudSQLDiskGB,
		},
	}
}

type postgresInstances struct {
	clients *clients
	sql     *sqladmin.Service
	records keyvalue.Store
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

func (p *Provider) removePostgres(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
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
	listed, err := attempted(ctx, i.sql.Instances.List(i.clients.project).Filter(i.filter(database)).Context(ctx).Do)
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
	if current.DatabaseVersion != spec.version {
		return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid,
			"postgres %s asks for %s, and its Cloud SQL instance %s runs %s: a major version changes by an upgrade ocel does not run.\n"+
				"Declare %s again, or upgrade it with `gcloud sql instances patch %s --database-version=%s` and deploy again",
			resource.Name, spec.version, current.Name, current.DatabaseVersion, current.DatabaseVersion, current.Name, spec.version)
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
			provider.PropertyHost:     addressOf(current, i.clients.NetworkPath(i.ref.Tier)),
			provider.PropertyPort:     strconv.Itoa(postgresPort),
			provider.PropertyDatabase: postgresDatabaseName,
			provider.PropertyUsername: postgresAccount,
			provider.PropertyPassword: password.plain,
			provider.PropertyTLSMode:  bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE.String(),
		},
	}, nil
}

func (i postgresInstances) desiredInstance(name string, spec postgresSpec, database string) *sqladmin.DatabaseInstance {
	project, grows := i.clients.project, true
	return &sqladmin.DatabaseInstance{
		Name:            name,
		Region:          i.clients.region,
		DatabaseVersion: spec.version,
		InstanceType:    cloudSQLInstanceType,
		Settings: &sqladmin.Settings{
			Edition:             cloudSQLEdition,
			Tier:                cloudSQLTier,
			AvailabilityType:    cloudSQLZonal,
			DataDiskType:        cloudSQLDiskType,
			DataDiskSizeGb:      cloudSQLDiskGB,
			StorageAutoResize:   &grows,
			UserLabels:          i.labels(database),
			BackupConfiguration: &sqladmin.BackupConfiguration{Enabled: true},
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
			DeletionProtectionEnabled: false,
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
	started, err := attempted(ctx, i.sql.Instances.Insert(i.clients.project, i.desiredInstance(name, spec, database)).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("create the Cloud SQL instance postgres %s runs on: %w", database, err)
	}
	if err := i.awaited(ctx, "creating postgres "+database, started); err != nil {
		return nil, err
	}
	return i.readInstance(ctx, database, name)
}

func (i postgresInstances) readInstance(ctx context.Context, database, name string) (*sqladmin.DatabaseInstance, error) {
	instance, err := attempted(ctx, i.sql.Instances.Get(i.clients.project, name).Context(ctx).Do)
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
	if current.State == cloudSQLRunnable {
		return current, nil
	}
	return nil, refusal.Refuse(refusal.CodeNotReady,
		"postgres %s runs on the Cloud SQL instance %s, which is %s, and a database is bound only once it is %s.\n"+
			"Start it with `gcloud sql instances patch %s --activation-policy=ALWAYS` if it was stopped, and deploy again",
		database, current.Name, current.State, cloudSQLRunnable, current.Name)
}

func addressOf(instance *sqladmin.DatabaseInstance, network string) string {
	if instance.Settings == nil || instance.Settings.IpConfiguration == nil || instance.Settings.IpConfiguration.PscConfig == nil {
		return ""
	}
	for _, auto := range instance.Settings.IpConfiguration.PscConfig.PscAutoConnections {
		if auto.ConsumerNetwork == network && auto.IpAddress != "" {
			return auto.IpAddress
		}
	}
	return ""
}

func (i postgresInstances) awaitAddress(ctx context.Context, database string, current *sqladmin.DatabaseInstance) (*sqladmin.DatabaseInstance, error) {
	network := i.clients.NetworkPath(i.ref.Tier)
	if addressOf(current, network) != "" {
		return current, nil
	}
	reached, err := until(ctx, "postgres "+database+" to be given an address on "+i.clients.Network(i.ref.Tier),
		func() (*sqladmin.DatabaseInstance, error) { return i.readInstance(ctx, database, current.Name) },
		func(instance *sqladmin.DatabaseInstance) bool { return addressOf(instance, network) != "" })
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
			return attempted(ctx, i.sql.Operations.Get(i.clients.project, name).Context(ctx).Do)
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

func (i postgresInstances) ensureDatabase(ctx context.Context, instance string) error {
	_, err := attempted(ctx, i.sql.Databases.Get(i.clients.project, instance, postgresDatabaseName).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !absent(err) {
		return fmt.Errorf("read the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
	}
	started, err := attempted(ctx, i.sql.Databases.Insert(i.clients.project, instance, &sqladmin.Database{Name: postgresDatabaseName}).Context(ctx).Do)
	if taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s database on Cloud SQL instance %s: %w", postgresDatabaseName, instance, err)
	}
	return i.awaited(ctx, "creating the "+postgresDatabaseName+" database on "+instance, started)
}

func (i postgresInstances) setUserPassword(ctx context.Context, instance, password string) error {
	_, err := attempted(ctx, i.sql.Users.Get(i.clients.project, instance, postgresAccount).Context(ctx).Do)
	switch {
	case absent(err):
		started, err := attempted(ctx, i.sql.Users.Insert(i.clients.project, instance, &sqladmin.User{Name: postgresAccount, Password: password}).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("create the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
		}
		return i.awaited(ctx, "creating the "+postgresAccount+" user on "+instance, started)
	case err != nil:
		return fmt.Errorf("read the %s user on Cloud SQL instance %s: %w", postgresAccount, instance, err)
	}
	started, err := attempted(ctx, i.sql.Users.Update(i.clients.project, instance, &sqladmin.User{Password: password}).Name(postgresAccount).Context(ctx).Do)
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

func (i postgresInstances) passwordKey(database string) keyvalue.Key {
	return keyvalue.Partition{Tier: i.ref.Tier, Root: keyvalue.RootPostgres}.Key(i.ref.Project, i.ref.Name.Env, database)
}

func (i postgresInstances) passwordBound(database string) seal.AssociatedData {
	return seal.AssociatedData{
		{Name: "project", Value: i.ref.Project},
		{Name: "tier", Value: string(i.ref.Tier)},
		{Name: "environment", Value: i.ref.Name.Env},
		{Name: "folder", Value: recordedPasswordFolder},
		{Name: "binding", Value: database},
		{Name: "name", Value: postgresSecretName},
	}
}

func (i postgresInstances) ensurePassword(ctx context.Context, database string) (recordedPassword, error) {
	key := i.passwordKey(database)
	err := keyvalue.Change(ctx, i.records, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
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
	entry, err := i.records.Read(ctx, key)
	if err != nil {
		return recordedPassword{}, fmt.Errorf("read the password recorded for postgres %s: %w", database, err)
	}
	var recorded recordedPassword
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return recordedPassword{}, fmt.Errorf("read the password recorded for postgres %s: %w", database, err)
	}
	opened, err := i.cipher.Open(ctx, i.ref.Tier, i.passwordBound(database), recorded.Sealed)
	if err != nil {
		return recordedPassword{}, fmt.Errorf("open the password recorded for postgres %s: %w", database, err)
	}
	recorded.plain = string(opened)
	return recorded, nil
}

func (i postgresInstances) recordPasswordSet(ctx context.Context, database, instance string) error {
	return keyvalue.Change(ctx, i.records, i.passwordKey(database), func(entry keyvalue.Entry) ([]byte, bool, error) {
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
		ensureProgress(progress).Say("Removing postgres " + binding.Name + ", its Cloud SQL instance " + current.Name + " and its data")
		started, err := attempted(ctx, i.sql.Instances.Delete(i.clients.project, current.Name).Context(ctx).Do)
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
	return keyvalue.Forget(ctx, i.records, i.passwordKey(binding.Name))
}
