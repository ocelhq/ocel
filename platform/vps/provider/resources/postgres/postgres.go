package postgres

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/officialimages"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const (
	postgresKind      = "pg"
	postgresPort      = "5432"
	Superuser         = "postgres"
	postgresData      = "/var/lib/postgresql/data"
	postgresSecretEnv = "POSTGRES_PASSWORD"

	postgresSecretName = "password"
)

var ownEnv = []string{
	"PGDATA",
	"POSTGRES_DB", "POSTGRES_DB_FILE",
	"POSTGRES_HOST_AUTH_METHOD",
	"POSTGRES_PASSWORD", "POSTGRES_PASSWORD_FILE",
	"POSTGRES_USER", "POSTGRES_USER_FILE",
}

var shape = component.Shape{Kind: "postgres", OwnEnv: ownEnv}

type Resource struct{ box component.Box }

func New(box component.Box) Resource { return Resource{box: box} }

var postgresCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}

func ResolveContainer(in resources.ProvisionRequest) (container.Spec, error) {
	version := images.DefaultPostgresVersion
	if in.Resource.Postgres != nil && in.Resource.Postgres.Version != "" {
		version = in.Resource.Postgres.Version
	}
	image, pinned := images.Postgres(version)
	if !pinned {
		return container.Spec{}, refusal.Refuse(refusal.CodeInvalid,
			"postgres %s asks for version %q; supported: %s",
			in.Resource.Name, version, strings.Join(images.PostgresVersions(), ", "))
	}
	return NewContainer(in.Ref, in.Resource.Name, postgresKind, officialimages.QualifyImage(image),
		container.Volume{Path: postgresData, Generation: version})
}

func NewContainer(ref provider.StackRef, database, kind, image string, volume container.Volume) (container.Spec, error) {
	name, err := container.Name(ref, database, kind)
	if err != nil {
		return container.Spec{}, err
	}
	return container.Spec{
		Name:     name,
		Project:  ref.Project,
		Resource: database,
		Tier:     ref.Tier,

		Image:        image,
		Env:          map[string]string{"POSTGRES_DB": database},
		Capabilities: postgresCapabilities,

		Volume: volume,
		Credential: container.Credential{
			Env: postgresSecretEnv,
			Reassert: func(secret string) ([]string, string) {
				return []string{"psql", "-U", Superuser, "-v", "ON_ERROR_STOP=1"},
					"ALTER USER " + Superuser + " PASSWORD '" + secret + "';\n"
			},
		},
		Ready:  []string{"pg_isready", "-h", "127.0.0.1", "-U", Superuser},
		Backup: boxapi.BackupPostgres,
	}, nil
}

func (r Resource) container(ctx context.Context, in resources.ProvisionRequest) (container.Spec, error) {
	spec, err := ResolveContainer(in)
	if err != nil {
		return container.Spec{}, err
	}
	return r.box.Reshaped(ctx, in, shape, spec)
}

func newPostgresBinding(in resources.ProvisionRequest, spec container.Spec, secret string) provider.Binding {
	return provider.Binding{
		Type:     provider.BindingPostgres,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     spec.Name,
			provider.PropertyPort:     postgresPort,
			provider.PropertyDatabase: in.Resource.Name,
			provider.PropertyUsername: Superuser,
			provider.PropertyPassword: secret,
		},
	}
}

func (r Resource) Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	spec, err := r.container(ctx, in)
	if err != nil {
		return "", "", err
	}
	action, reason, err := r.box.Containers.Plan(ctx, spec)
	if err != nil || action == provider.ActionCreate {
		return action, reason, err
	}
	var change component.Change
	change.Add(action, reason)
	sealed, err := r.box.Containers.Kept(ctx, in.Ref.Tier, spec.Name)
	if err != nil {
		return "", "", err
	}
	if len(sealed) == 0 {
		change.Add(provider.ActionUpdate, "the box keeps no password for it, so a new one is minted that its running server was never handed")
	}
	change.AddRecorded(recorded, newPostgresBinding(in, spec, ""))
	return change.Report()
}

func (r Resource) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	spec, err := r.container(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	progress.Say("Provisioning postgres " + in.Resource.Name + " in container " + spec.Name)
	secret, err := r.secret(ctx, in, spec.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := r.box.Containers.Run(ctx, spec, secret); err != nil {
		return provider.Binding{}, err
	}
	return newPostgresBinding(in, spec, secret), nil
}

func (r Resource) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name, err := container.Name(ref, binding.Name, postgresKind)
	if err != nil {
		return err
	}
	progress.Say("Removing postgres " + binding.Name + ", its container " + name + " and its data")
	return r.box.Containers.Remove(ctx, container.Ref{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
}

func NewSecretAssociatedData(ref provider.StackRef, resource string) (seal.AssociatedData, error) {
	return boxapi.NewSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String(), component.SecretFolder, resource, postgresSecretName)
}

func (r Resource) secret(ctx context.Context, in resources.ProvisionRequest, name string) (string, error) {
	bound, err := NewSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", err
	}
	return r.box.KeptSecret(ctx, in, name, bound)
}
