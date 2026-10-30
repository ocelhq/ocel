package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func (p *Provider) ensureKeys(ctx context.Context, tier environment.Tier, addedBy string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.EnsurePart(ctx, tier, addedBy, bootstrap.Keys.For(b, tier))
}
