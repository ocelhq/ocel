package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) ImageRegistry(ctx context.Context, class providerkit.Class, _ []string) (providerkit.RegistryTarget, error) {
	if p.clients.emulated() {
		return providerkit.RegistryTarget{}, nil
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return providerkit.RegistryTarget{}, err
	}
	names := p.Names()
	return providerkit.RegistryTarget{
		Server:    p.options.Region + dockerRegistryHost,
		Namespace: names.project + "/" + names.Repository(class),
		Username:  registryUser,
		Password:  token,
	}, nil
}

func (p *Provider) Images(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	return providerkit.RegistryImages(target), nil
}

func (p *Provider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return providerkit.DaemonImages(), nil
}

var (
	_ providerkit.ImageRegistry = (*Provider)(nil)
	_ providerkit.ImagePusher   = (*Provider)(nil)
	_ providerkit.ImageLoader   = (*Provider)(nil)
)
