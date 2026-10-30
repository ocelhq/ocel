package notfoundapi

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(records tier.Records, c API, namespace string, wildcard edge.Kind) tier.Part {
	return tier.Recorded(records, tier.RecordedSpec{
		Name:      Part,
		Summary:   Summary,
		Inventory: func(tier environment.Tier) []pricing.Item { return Inventory(namespace, tier) },
		Work: func(_ context.Context, tier environment.Tier) (tier.Work, error) {
			return Work(c, namespace, tier), nil
		},
		Remove: func(ctx context.Context, _ environment.Tier, recorded []provider.PhysicalResource) error {
			return Remove(ctx, c, recorded)
		},
		Wildcard: wildcard,
	})
}

func Work(c API, namespace string, tier environment.Tier) (work tier.Work) {
	work.Digest = Digest(namespace, tier)
	work.Make = func(ctx context.Context, prior []provider.PhysicalResource, _ bool) ([]provider.PhysicalResource, error) {
		return Ensure(ctx, c, namespace, tier, prior)
	}
	return work
}
