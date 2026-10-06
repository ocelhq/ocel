package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type cloudflareFront struct {
	*cloudflare.Proxy
	origin    edge.Edge
	options   cloudflare.Options
	wildcards originWildcards
}

func (f cloudflareFront) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if base := f.options.OriginBase(spec.Tier); f.Facts().RunsCode && !spec.PruneOnly && base != "" {
		if err := f.wildcards.ensure(ctx, spec.Tier, base, spec.Warn); err != nil {
			return nil, err
		}
	}
	return f.Proxy.Reconcile(ctx, spec, prior)
}

func (f cloudflareFront) Hooks() edge.Hooks {
	hooks := f.Proxy.Hooks()
	origin := f.origin.Hooks()
	hooks.CheckBootstrapInstalled = origin.CheckBootstrapInstalled
	hooks.ListBoundHostnames = origin.ListBoundHostnames
	return hooks
}

func (f cloudflareFront) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if _, err := f.origin.Bootstrap(ctx, tier); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return f.Proxy.Bootstrap(ctx, tier)
}

func (f cloudflareFront) Teardown(ctx context.Context, tier environment.Tier) error {
	if err := f.wildcards.destroy(ctx, tier); err != nil {
		return err
	}
	return f.origin.Teardown(ctx, tier)
}
