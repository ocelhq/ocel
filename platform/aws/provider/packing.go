package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) PackApp(ctx context.Context, packing providerkit.AppPacking, progress providerkit.Progress) (providerkit.AppPack, error) {
	return p.stacks.PackApp(ctx, packing, progress)
}
