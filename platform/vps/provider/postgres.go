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

func newPostgresContainer(ref provider.StackRef, database, kind, image string, volume host.Volume) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     host.ResourceName(ref.Project, ref.Name.String(), database, kind),
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
		Ready:    []string{"pg_isready", "-h", "127.0.0.1", "-U", postgresSuperuser},
		Backup:   host.BackupPostgres,
		Database: database,
	}
}

func (p *Provider) ProvisionPostgres(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	spec, err := resolvePostgresContainer(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if spec, err = p.reshaped(ctx, in, transformTypePostgres, spec); err != nil {
		return provider.Binding{}, err
	}
	if progress != nil {
		progress.Say("Provisioning postgres " + in.Resource.Name + " in container " + spec.Name)
	}
	secret, err := p.postgresSecret(ctx, in, spec.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := p.host.RunResource(ctx, spec, secret); err != nil {
		return provider.Binding{}, err
	}
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
	}, nil
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
