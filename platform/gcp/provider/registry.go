package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) EnsureImageRegistry(ctx context.Context, class edge.Class, _ []string) (provider.RegistryTarget, error) {
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
		Namespace: names.project + "/" + names.Repository(class),
		Username:  registryUser,
		Password:  token,
	}, nil
}
