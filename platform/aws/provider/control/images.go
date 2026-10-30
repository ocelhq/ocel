package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func refuseOpeningRegistryImages(context.Context, provider.RegistryTarget) (provider.ImageStore, error) {
	return nil, provider.RefuseNeedsRunner("pushing images")
}

func refuseEnsuringImageRegistry(context.Context, environment.Tier) (provider.RegistryTarget, error) {
	return provider.RegistryTarget{}, provider.RefuseNeedsRunner("making the image registry")
}
