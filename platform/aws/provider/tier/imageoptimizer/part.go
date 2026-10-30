package imageoptimizer

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(records tier.Records, c Clients, namespace string, specFor func(context.Context, environment.Tier) (Spec, error)) tier.Part {
	described := description(namespace, specFor)
	return tier.Recorded(records, tier.RecordedSpec{
		Name:      described.Name,
		Summary:   described.Summary,
		Inventory: described.Inventory,
		Work: func(ctx context.Context, tier environment.Tier) (work tier.Work, err error) {
			spec, err := specFor(ctx, tier)
			if err != nil {
				return work, err
			}
			return Work(c, spec), nil
		},
		Remove: func(ctx context.Context, tier environment.Tier, _ []provider.PhysicalResource) error {
			return Remove(ctx, c, namespace, tier)
		},
		IsUsedBy: isUsedBy,
	})
}

func DescribedPart(records tier.Records, namespace string, specFor func(context.Context, environment.Tier) (Spec, error)) tier.Part {
	return tier.Described(records, description(namespace, specFor))
}

func description(namespace string, specFor func(context.Context, environment.Tier) (Spec, error)) tier.Description {
	return tier.Description{
		Name:      Part,
		Summary:   Summary,
		Inventory: func(tier environment.Tier) []pricing.Resource { return Inventory(namespace, tier) },
		Digest: func(ctx context.Context, tier environment.Tier) (string, error) {
			spec, err := specFor(ctx, tier)
			if err != nil {
				return "", err
			}
			return Digest(spec), nil
		},
	}
}

func isUsedBy(uses []buildoutput.Uses, _ edge.Kind) bool {
	return slices.ContainsFunc(uses, func(each buildoutput.Uses) bool { return each.ImageOptimization })
}

func Work(c Clients, spec Spec) tier.Work {
	return tier.Work{
		Digest: Digest(spec),
		Make: func(ctx context.Context, _ []provider.PhysicalResource, updatesTrust bool) ([]provider.PhysicalResource, error) {
			spec := spec
			spec.UpdatesTrust = updatesTrust
			return Ensure(ctx, c, spec)
		},
	}
}

func ReadURL(ctx context.Context, records tier.Records, tier environment.Tier) (string, error) {
	recorded, present, err := records.Read(ctx, tier, Part)
	if err != nil || !present || recorded.State != stackrecords.PartReady {
		return "", err
	}
	return URLOf(recorded.Resources), nil
}
