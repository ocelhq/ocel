package tier

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type SharedSpec struct {
	Part          string
	RefuseInUse   func(environment.Tier, []string) error
	Summary       string
	RemovalReason string
	Shape         func(tier environment.Tier) []pricing.Shaped
	Describe      func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error)
	Remove        func(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

func Shared(spec SharedSpec) Part {
	removeRaised := func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
		_, present, err := spec.Describe(ctx, tier)
		if err != nil || !present {
			return err
		}
		return spec.Remove(ctx, tier, progress)
	}
	return Part{
		Name:     spec.Part,
		Summary:  spec.Summary,
		Describe: spec.Describe,
		Shape:    spec.Shape,
		Teardown: &Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				part, present, err := spec.Describe(ctx, tier)
				if err != nil || !present {
					return nil, err
				}
				return Deletion(part.Name, "the bootstrap it belongs to is being removed", part.Resources), nil
			},
			Remove: removeRaised,
		},
		Removal: &Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				part, present, err := spec.Describe(ctx, tier)
				if err != nil || !present {
					return nil, err
				}
				if len(part.UsedBy) > 0 {
					return nil, spec.RefuseInUse(tier, part.UsedBy)
				}
				return Deletion(part.Name, spec.RemovalReason, part.Resources), nil
			},
			Remove: removeRaised,
		},
	}
}
