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
	providerkit.Bootstrapper
	elevated func(context.Context) error
}

func (e elevating) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if !req.Heal {
		if err := e.elevated(ctx); err != nil {
			return err
		}
	}
	return e.Bootstrapper.Apply(ctx, req, report)
}

func (e elevating) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrapper.Remove(ctx, class, report)
}
