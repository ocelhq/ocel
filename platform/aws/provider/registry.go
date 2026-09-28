package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func (p *Provider) EnsureImageRegistry(ctx context.Context, _ edge.Class, _ []string) (provider.RegistryTarget, error) {
	return registry.Resolve(ctx, ecr.NewFromConfig(p.aws))
}
