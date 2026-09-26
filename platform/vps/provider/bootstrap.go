package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
}

func (e elevating) Apply(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	if !req.Heal {
		if err := e.elevated(ctx); err != nil {
			return err
		}
	}
	return e.Bootstrap.Apply(ctx, req, progress)
}

func (e elevating) Remove(ctx context.Context, class edge.Class, progress edge.Progress) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrap.Remove(ctx, class, progress)
}
