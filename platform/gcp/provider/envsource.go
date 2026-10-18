package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
)

func (p *Provider) EnsureEnvSourceSync(ctx context.Context, tier environment.Tier, project string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return envsource.Ensure(ctx, b, tier, project)
}
