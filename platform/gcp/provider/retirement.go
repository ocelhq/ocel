package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) PlanRetirement(ctx context.Context, live provider.LiveApps, progress progress.Log) (provider.Plan, error) {
	return p.release().PlanRetirement(ctx, live, progress)
}

func (p *Provider) Retire(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
	return p.release().Retire(ctx, live, progress)
}
