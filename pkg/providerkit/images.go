package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func imageStoreFor(ctx context.Context, p provider.Provider, target images.Registry) (images.Store, error) {
	hooks := p.Hooks()
	if !target.Named() {
		if hooks.OpenDirectImages == nil {
			return nil, nil
		}
		return hooks.OpenDirectImages(ctx)
	}
	if hooks.OpenRegistryImages != nil {
		return hooks.OpenRegistryImages(ctx, target)
	}
	return images.RegistryStore(target), nil
}
