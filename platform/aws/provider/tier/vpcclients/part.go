package vpcclients

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(records tier.Records, api API, namespace string) tier.Part {
	return tier.Recorded(records, tier.RecordedSpec{
		Name:    Part,
		Summary: Summary,
		Shape: func(at environment.Tier) []pricing.Shaped {
			return []pricing.Shaped{{Name: NameFor(namespace, at), Type: component.TerraformSecurityGroup, Properties: map[string]any{}}}
		},
		Work: func(_ context.Context, at environment.Tier) (tier.Work, error) {
			return Work(api, namespace, at, records.Region), nil
		},
		Remove: func(ctx context.Context, _ environment.Tier, recorded []provider.PhysicalResource) error {
			return Remove(ctx, api, recorded)
		},
	})
}

func Work(api API, namespace string, at environment.Tier, region string) tier.Work {
	return tier.Work{
		Digest: Digest(namespace, at),
		Make: func(ctx context.Context, _ []provider.PhysicalResource, _ bool) ([]provider.PhysicalResource, error) {
			return Ensure(ctx, api, namespace, at, region)
		},
	}
}
