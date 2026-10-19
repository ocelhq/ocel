package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) PlanRetirement(ctx context.Context, live provider.LiveApps, progress progress.Log) (provider.Plan, error) {
	if held, err := p.holdsBootstrap(ctx, live.Tier); err != nil || !held {
		return provider.Plan{}, err
	}
	return p.stacks.PlanWorkersRetirement(ctx, live, progress)
}

func (p *Provider) Retire(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
	if held, err := p.holdsBootstrap(ctx, live.Tier); err != nil || !held {
		return err
	}
	return p.stacks.RetireWorkers(ctx, live, progress)
}

func (p *Provider) holdsBootstrap(ctx context.Context, tier environment.Tier) (bool, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	return deployed.Present, err
}
