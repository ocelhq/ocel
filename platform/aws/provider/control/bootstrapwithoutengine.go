package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

type bootstrapWithoutEngine struct {
	cfnParts         func() []provider.Part
	describe         func(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error)
	describeCFNParts func(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error)
	tierParts        tier.Parts
}

func (b bootstrapWithoutEngine) Parts() []provider.Part {
	return append(b.cfnParts(), b.tierParts.Offered()...)
}

func (b bootstrapWithoutEngine) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	return b.describe(ctx, tier)
}

func (b bootstrapWithoutEngine) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	parts, err := b.describeCFNParts(ctx, described)
	if err != nil {
		return nil, err
	}
	return describeTierParts(ctx, described, parts, b.tierParts)
}

func (bootstrapWithoutEngine) Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning a bootstrap")
}

func (bootstrapWithoutEngine) Apply(context.Context, provider.BootstrapRequest, progress.Log) error {
	return provider.RefuseNeedsRunner("applying a bootstrap")
}

func (bootstrapWithoutEngine) PlanRemove(context.Context, environment.Tier) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning a bootstrap's removal")
}

func (bootstrapWithoutEngine) Remove(context.Context, environment.Tier, progress.Log) error {
	return provider.RefuseNeedsRunner("removing a bootstrap")
}
