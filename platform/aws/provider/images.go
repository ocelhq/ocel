package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func (p *Provider) EnsureImageRegistry(ctx context.Context, _ providerkit.Class, _ []string) (providerkit.RegistryTarget, error) {
	return registry.Resolve(ctx, ecr.NewFromConfig(p.aws))
}

func (p *Provider) RegistryImages(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	if !registry.Owns(target) {
		return providerkit.RegistryImages(target), nil
	}
	return registry.Images(target, ecr.NewFromConfig(p.aws)), nil
}
