package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) EnsureImageRegistry(ctx context.Context, class providerkit.Class, _ []string) (providerkit.RegistryTarget, error) {
	if p.emulated() {
		return providerkit.RegistryTarget{}, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return providerkit.RegistryTarget{}, err
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return providerkit.RegistryTarget{}, err
	}
	return providerkit.RegistryTarget{
		Server:    p.options.Region + dockerRegistryHost,
		Namespace: names.project + "/" + names.Repository(class),
		Username:  registryUser,
		Password:  token,
	}, nil
}
