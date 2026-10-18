package vps

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/officialimages"
)

const (
	postgresKind      = "pg"
	postgresPort      = "5432"
	postgresSuperuser = "postgres"
	postgresData      = "/var/lib/postgresql/data"
	postgresSecretEnv = "POSTGRES_PASSWORD"

	postgresSecretName = "password"
)

var postgresCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}

func resolvePostgresContainer(in resources.ProvisionRequest) (host.ResourceContainer, error) {
	version := images.DefaultPostgresVersion
	if in.Resource.Postgres != nil && in.Resource.Postgres.Version != "" {
		version = in.Resource.Postgres.Version
	}
	image, pinned := images.Postgres(version)
	if !pinned {
		return host.ResourceContainer{}, refusal.Refuse(refusal.CodeInvalid,
			"postgres %s asks for version %q; supported: %s",
			in.Resource.Name, version, strings.Join(images.PostgresVersions(), ", "))
	}
	return newPostgresContainer(in.Ref, in.Resource.Name, postgresKind, officialimages.QualifyImage(image),
		host.Volume{Path: postgresData, Generation: version}), nil
}

func namePostgres(ref provider.StackRef, database, kind string) string {
	return host.ResourceName(ref.Project, ref.Name.String(), database, kind)
}

func newPostgresContainer(ref provider.StackRef, database, kind, image string, volume host.Volume) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     namePostgres(ref, database, kind),
		Project:  ref.Project,
		Resource: database,
		Tier:     ref.Tier,

		Image:        image,
		Env:          map[string]string{"POSTGRES_DB": database},
		Capabilities: postgresCapabilities,

		Volume: volume,
		Credential: host.Credential{
			Env: postgresSecretEnv,
			Reassert: func(secret string) ([]string, string) {
				return []string{"psql", "-U", postgresSuperuser, "-v", "ON_ERROR_STOP=1"},
					"ALTER USER " + postgresSuperuser + " PASSWORD '" + secret + "';\n"
			},
		},
		Ready:  []string{"pg_isready", "-h", "127.0.0.1", "-U", postgresSuperuser},
		Backup: host.BackupPostgres,
	}
}

func (p *Provider) postgresContainer(ctx context.Context, in resources.ProvisionRequest) (host.ResourceContainer, error) {
	spec, err := resolvePostgresContainer(in)
	if err != nil {
		return host.ResourceContainer{}, err
	}
	return p.reshaped(ctx, in, transformTypePostgres, spec)
}

func newPostgresBinding(in resources.ProvisionRequest, spec host.ResourceContainer, secret string) provider.Binding {
	return provider.Binding{
		Type:     provider.BindingPostgres,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     spec.Name,
			provider.PropertyPort:     postgresPort,
			provider.PropertyDatabase: in.Resource.Name,
			provider.PropertyUsername: postgresSuperuser,
			provider.PropertyPassword: secret,
		},
	}
}

func (p *Provider) PlanPostgres(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	spec, err := p.postgresContainer(ctx, in)
	if err != nil {
		return "", "", err
	}
	action, reason, err := p.host.PlanResource(ctx, spec)
	if err != nil || action == provider.ActionCreate {
		return action, reason, err
	}
	var change resourceChange
	change.add(action, reason)
	sealed, err := p.host.Kept(ctx, in.Ref.Tier, spec.Name)
	if err != nil {
		return "", "", err
	}
	if len(sealed) == 0 {
		change.add(provider.ActionUpdate, "the box keeps no password for it, so a new one is minted that its running server was never handed")
	}
	change.addRecorded(recorded, newPostgresBinding(in, spec, ""))
	return change.report()
}

func (p *Provider) ProvisionPostgres(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	spec, err := p.postgresContainer(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	progress.Say("Provisioning postgres " + in.Resource.Name + " in container " + spec.Name)
	secret, err := p.postgresSecret(ctx, in, spec.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := p.host.RunResource(ctx, spec, secret); err != nil {
		return provider.Binding{}, err
	}
	return newPostgresBinding(in, spec, secret), nil
}

func (p *Provider) RemovePostgres(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name := namePostgres(ref, binding.Name, postgresKind)
	progress.Say("Removing postgres " + binding.Name + ", its container " + name + " and its data")
	return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
}

func newPostgresSecretAssociatedData(ref provider.StackRef, resource string) (seal.AssociatedData, error) {
	return live.NewSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String(), resourceSecretFolder, resource, postgresSecretName)
}

func (p *Provider) postgresSecret(ctx context.Context, in resources.ProvisionRequest, name string) (string, error) {
	bound, err := newPostgresSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", err
	}
	return p.keptSecret(ctx, in, name, bound)
}
