package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
)

func (p *Provider) removePartUser(ctx context.Context, tier environment.Tier, project string, progress progress.Log) error {
	consumer, err := records.ReadOrEmpty(ctx, p.KeyValues(), albConsumerAt(tier, project))
	if err != nil || len(consumer.Value) == 0 {
		return err
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.removePartUser(ctx, tier, project, progress)
}

func (b bootstrap) removePartUser(ctx context.Context, tier environment.Tier, project string, progress progress.Log) error {
	return b.releaseALB(ctx, tier, project, true, progress)
}
