package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func (p *Provider) OpenRegistryImages(_ context.Context, target images.RegistryTarget) (images.ImageStore, error) {
	if !registry.Owns(target) {
		return images.RegistryImages(target), nil
	}
	return registry.Images(target, ecr.NewFromConfig(p.aws)), nil
}
