package vps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func postgresContainer(in resources.ProvisionRequest) (host.ResourceContainer, error) {
	version := constants.DefaultPostgresVersion
	if in.Resource.Postgres != nil && in.Resource.Postgres.Version != "" {
		version = in.Resource.Postgres.Version
	}
	image, pinned := constants.PostgresImage(version)
	if !pinned {
		return host.ResourceContainer{}, refusal.Refuse(refusal.CodeInvalid,
			"postgres %s asks for version %q; supported: %s",
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
		Ready:    []string{"pg_isready", "-h", "127.0.0.1", "-U", postgresSuperuser},
		Backup:   host.BackupPostgres,
		Database: in.Resource.Name,
	}, nil
}

func mintPostgresSecret() (string, error) {
	raw := make([]byte, postgresSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a postgres password: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (p *Provider) ProvisionPostgres(ctx context.Context, in resources.ProvisionRequest, progress edge.Progress) (provider.Binding, error) {
	spec, err := postgresContainer(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if spec, err = p.reshaped(ctx, in, transformTypePostgres, spec); err != nil {
		return provider.Binding{}, err
	}
	if progress != nil {
		progress.Say("Provisioning postgres " + in.Resource.Name + " as " + spec.Name)
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

func (p *Provider) postgresSecret(ctx context.Context, in resources.ProvisionRequest, name string) (string, error) {
	return p.recordedSecret(ctx, in, name, postgresSecretFolder, postgresSecretName, mintPostgresSecret)
}

func (p *Provider) recordedSecret(ctx context.Context, in resources.ProvisionRequest, name, folder, item string, mint func() (string, error)) (string, error) {
	at := records.SealScope{
		Project: in.Ref.Project, Class: in.Ref.Class, Env: in.Ref.Name.String(),
		Folder: folder, Binding: in.Resource.Name, Name: item,
	}
	sealed, err := p.host.Kept(ctx, in.Ref.Class, name)
	if err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		minted, err := mint()
		if err != nil {
			return "", err
		}
		candidate, err := p.cipher.Seal(ctx, at, []byte(minted))
		if err != nil {
			return "", err
		}
		if sealed, err = p.host.KeepOnce(ctx, in.Ref.Class, name, candidate); err != nil {
			return "", err
		}
	}
	opened, err := p.cipher.Open(ctx, at, sealed)
	if err != nil {
		return "", err
	}
	if len(opened) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			in.Resource.Name, host.KeptPath(in.Ref.Class, name))
	}
	return string(opened), nil
}
