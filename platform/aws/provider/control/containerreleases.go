package control

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/containerservice"
)

func (p *Provider) ScaleUpContainerReleases(ctx context.Context, promoted provider.PromotedReleases, progress progress.Log) error {
	return p.releaseScaling().ScaleUp(ctx, promoted, progress)
}

func (p *Provider) ScaleDownContainerReleases(ctx context.Context, demoted provider.DemotedReleases, progress progress.Log) error {
	return p.releaseScaling().ScaleDown(ctx, demoted, progress)
}

func (p *Provider) releaseScaling() containerservice.ReleaseScaling {
	return containerservice.ReleaseScaling{
		Namespace:   string(p.Namespace),
		AutoScaling: applicationautoscaling.NewFromConfig(p.Config),
		Services:    ecs.NewFromConfig(p.Config),
		Balancers:   elasticloadbalancingv2.NewFromConfig(p.Config),
		Now:         time.Now,
		Wait:        waitBetweenHealthReads,
	}
}

func waitBetweenHealthReads(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
