package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	providerkit.Bootstrap
	elevated func(context.Context) error
}

func (e elevating) Apply(ctx context.Context, req providerkit.BootstrapRequest, progress providerkit.Progress) error {
	if !req.Heal {
		if err := e.elevated(ctx); err != nil {
			return err
		}
	}
	return e.Bootstrap.Apply(ctx, req, progress)
}

func (e elevating) Remove(ctx context.Context, class providerkit.Class, progress providerkit.Progress) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrap.Remove(ctx, class, progress)
}
