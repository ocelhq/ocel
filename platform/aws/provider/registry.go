package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) EnsureImageRegistry(ctx context.Context, _ edge.Class, _ []string) (provider.RegistryTarget, error) {
	return registry.Resolve(ctx, ecr.NewFromConfig(p.aws))
}
