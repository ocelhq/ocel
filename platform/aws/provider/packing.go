package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) PackApp(ctx context.Context, packing provider.AppPacking, progress edge.Progress) (provider.AppPack, error) {
	return p.stacks.PackApp(ctx, packing, progress)
}
