package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) EnsureImageRegistry(ctx context.Context, tier environment.Tier) (provider.RegistryTarget, error) {
	if p.emulated() {
		return provider.RegistryTarget{}, nil
	}
	named, err := p.Names(ctx)
	if err != nil {
		return provider.RegistryTarget{}, err
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return provider.RegistryTarget{}, err
	}
	return provider.RegistryTarget{
		Server:    p.options.Region + names.DockerRegistryHost,
		Namespace: named.Project() + "/" + named.Repository(tier),
		Username:  registryUser,
		Password:  token,
	}, nil
}
