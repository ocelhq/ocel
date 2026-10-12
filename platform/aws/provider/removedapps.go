package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) PlanRemovedApps(ctx context.Context, live provider.LiveApps, progress progress.Log) (provider.Plan, error) {
	return p.stacks.PlanWorkersRetirement(ctx, live, progress)
}

func (p *Provider) RetireRemovedApps(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
	return p.stacks.RetireWorkers(ctx, live, progress)
}
