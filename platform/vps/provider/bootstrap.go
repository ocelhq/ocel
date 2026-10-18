package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) elevated(ctx context.Context) error {
	live, err := p.Session(ctx)
	if err != nil {
		return err
	}
	_, err = live.Preflight(ctx)
	return err
}

type checkedHost struct {
	host.Bootstrap
	checkHost func(context.Context, provider.HostCheckRequest) ([]provider.HostCheck, error)
}

func (b checkedHost) CheckHost(ctx context.Context, req provider.HostCheckRequest) ([]provider.HostCheck, error) {
	return b.checkHost(ctx, req)
}

func (checkedHost) RemovePartUser(context.Context, environment.Tier, string, progress.Log) error {
	return nil
}

type elevating struct {
	provider.Bootstrap
	elevated func(context.Context) error
	served   func(context.Context, environment.Tier) error
}

func (e elevating) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
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
