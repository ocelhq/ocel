package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func (p *Provider) EnsureImageRegistry(ctx context.Context, _ providerkit.Class, _ []string) (providerkit.RegistryTarget, error) {
	return registry.Resolve(ctx, ecr.NewFromConfig(p.aws))
}
