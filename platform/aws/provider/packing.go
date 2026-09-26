package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) PackApp(ctx context.Context, packing providerkit.AppPacking, progress edge.Progress) (providerkit.AppPack, error) {
	return p.stacks.PackApp(ctx, packing, progress)
}
