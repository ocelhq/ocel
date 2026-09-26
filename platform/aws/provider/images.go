package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func (p *Provider) OpenRegistryImages(_ context.Context, target provider.RegistryTarget) (provider.ImageStore, error) {
	if !registry.Owns(target) {
		return images.RegistryStore(target), nil
	}
	return registry.Images(target, ecr.NewFromConfig(p.aws)), nil
}
