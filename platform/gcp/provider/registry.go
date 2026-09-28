package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) EnsureImageRegistry(ctx context.Context, tier environment.Tier, _ []string) (provider.RegistryTarget, error) {
	if p.emulated() {
		return provider.RegistryTarget{}, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return provider.RegistryTarget{}, err
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return provider.RegistryTarget{}, err
	}
	return provider.RegistryTarget{
		Server:    p.options.Region + dockerRegistryHost,
		Namespace: names.project + "/" + names.Repository(tier),
		Username:  registryUser,
		Password:  token,
	}, nil
}
