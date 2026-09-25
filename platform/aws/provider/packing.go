package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) PackApp(ctx context.Context, packing providerkit.AppPacking, progress providerkit.Progress) (providerkit.AppPack, error) {
	return p.releases.PackApp(ctx, packing, progress)
}
