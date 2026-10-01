package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) elevated(ctx context.Context) error {
	live, err := p.Session(ctx)
	if err != nil {
		return err
	}
	_, err = live.Preflight(ctx)
	return err
}

type elevating struct {
	provider.Bootstrap
	elevated func(context.Context) error
	served   func(context.Context, environment.Tier) error
}

func (e elevating) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if req.Repair {
		return e.Bootstrap.Apply(ctx, req, progress)
	}
	if err := e.elevated(ctx); err != nil {
		return err
	}
	if err := e.Bootstrap.Apply(ctx, req, progress); err != nil || e.served == nil {
		return err
	}
	return e.served(ctx, req.Tier)
}

func (e elevating) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrap.Remove(ctx, tier, progress)
}
