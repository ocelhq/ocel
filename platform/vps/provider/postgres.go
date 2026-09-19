package vps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	postgresKind      = "pg"
	postgresRegistry  = "public.ecr.aws/docker/library/"
	postgresPort      = "5432"
	postgresSuperuser = "postgres"
	postgresData      = "/var/lib/postgresql/data"
	postgresSecretEnv = "POSTGRES_PASSWORD"
	postgresSecretLen = 24

	postgresSecretFolder = "resources"
	postgresSecretName   = "password"
)

var postgresCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}

func postgresContainer(in resources.Instruction) (host.ResourceContainer, error) {
	version := ""
	if in.Resource.Postgres != nil {
		version = in.Resource.Postgres.Version
	}
	image, pinned := constants.PostgresImage(version)
	if !pinned {
		return host.ResourceContainer{}, providerkit.Refuse(providerkit.CodeInvalid,
			"postgres %s asks for version %q, and a box runs %s: declare one of those",
			in.Resource.Name, version, strings.Join(constants.PostgresVersions(), ", "))
	}
	return host.ResourceContainer{
		Name:     host.ResourceName(in.Ref.Name.String(), in.Resource.Name, postgresKind),
		Project:  in.Ref.Project,
		Resource: in.Resource.Name,
		Class:    in.Ref.Class,

		Image:        postgresRegistry + image,
		Env:          map[string]string{"POSTGRES_DB": in.Resource.Name},
		Capabilities: postgresCapabilities,

		Volume: host.Volume{Path: postgresData, Generation: version},
		Credential: host.Credential{
			Env: postgresSecretEnv,
			Reassert: func(secret string) ([]string, string) {
				return []string{"psql", "-U", postgresSuperuser, "-v", "ON_ERROR_STOP=1"},
					"ALTER USER " + postgresSuperuser + " PASSWORD '" + secret + "';\n"
			},
		},
		Ready: []string{"pg_isready", "-h", "127.0.0.1", "-U", postgresSuperuser},
	}, nil
}

func mintPostgresSecret() (string, error) {
	raw := make([]byte, postgresSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a postgres password: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (p *Provider) Postgres(ctx context.Context, in resources.Instruction, report providerkit.Reporter) (providerkit.Binding, error) {
	spec, err := postgresContainer(in)
	if err != nil {
		return providerkit.Binding{}, err
	}
	if spec, err = p.reshaped(ctx, in, spec); err != nil {
		return providerkit.Binding{}, err
	}
	if report != nil {
		report.Say("Standing postgres " + in.Resource.Name + " up as " + spec.Name)
	}
	secret, err := p.held(ctx, in, spec.Name)
	if err != nil {
		return providerkit.Binding{}, err
	}
	if err := p.host.StandResource(ctx, spec, secret); err != nil {
		return providerkit.Binding{}, err
	}
	return providerkit.Binding{
		Type: providerkit.BindingPostgres,
		Name: in.Resource.Name,
		Properties: map[string]string{
			providerkit.PropertyHost:     spec.Name,
			providerkit.PropertyPort:     postgresPort,
			providerkit.PropertyDatabase: in.Resource.Name,
			providerkit.PropertyUsername: postgresSuperuser,
			providerkit.PropertyPassword: secret,
		},
	}, nil
}

func (p *Provider) held(ctx context.Context, in resources.Instruction, name string) (string, error) {
	at := providerkit.Coordinate{
		Project: in.Ref.Project, Class: in.Ref.Class, Env: in.Ref.Name.String(),
		Folder: postgresSecretFolder, Binding: in.Resource.Name, Name: postgresSecretName,
	}
	sealed, err := p.host.Kept(ctx, in.Ref.Class, name)
	if err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		minted, err := mintPostgresSecret()
		if err != nil {
			return "", err
		}
		candidate, err := p.sealer.Seal(ctx, at, []byte(minted))
		if err != nil {
			return "", err
		}
		if sealed, err = p.host.KeepOnce(ctx, in.Ref.Class, name, candidate); err != nil {
			return "", err
		}
	}
	opened, err := p.sealer.Open(ctx, at, sealed)
	if err != nil {
		return "", err
	}
	if len(opened) == 0 {
		return "", providerkit.Refuse(providerkit.CodeNotReady,
			"what this box keeps for postgres %s opens to nothing, so there is no password to bind an app to. Remove %s on the box and run this again",
			in.Resource.Name, host.KeptPath(in.Ref.Class, name))
	}
	return string(opened), nil
}

func (p *Provider) RemoveResource(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, report providerkit.Reporter) error {
	if binding.Type != providerkit.BindingPostgres {
		return nil
	}
	name := host.ResourceName(ref.Name.String(), binding.Name, postgresKind)
	if report != nil {
		report.Say("Taking postgres " + binding.Name + " and its data down")
	}
	return p.host.RemoveResource(ctx, ref.Class, name)
}

var (
	_ resources.Postgres = (*Provider)(nil)
	_ resources.Remover  = (*Provider)(nil)
)
