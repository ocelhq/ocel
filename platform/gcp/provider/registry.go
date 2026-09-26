package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const registryUser = "oauth2accesstoken"

func (p *Provider) EnsureImageRegistry(ctx context.Context, class edge.Class, _ []string) (images.Registry, error) {
	if p.emulated() {
		return images.Registry{}, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return images.Registry{}, err
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return images.Registry{}, err
	}
	return images.Registry{
		Server:    p.options.Region + dockerRegistryHost,
		Namespace: names.project + "/" + names.Repository(class),
		Username:  registryUser,
		Password:  token,
	}, nil
}
