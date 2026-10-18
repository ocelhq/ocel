package tier

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

type SharedSpec struct {
	Stack         sharedstack.Stack
	Summary       string
	RemovalReason string
	Describe      func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error)
	Remove        func(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

func Shared(spec SharedSpec) Part {
	return Part{
		Name:     spec.Stack.Part,
		Summary:  spec.Summary,
		Describe: spec.Describe,
		Removal: &Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				part, present, err := spec.Describe(ctx, tier)
				if err != nil || !present {
					return nil, err
				}
				if len(part.UsedBy) > 0 {
					return nil, spec.Stack.RefuseInUse(tier, part.UsedBy)
				}
				return Deletion(part.Name, spec.RemovalReason, part.Resources), nil
			},
			Remove: spec.Remove,
		},
	}
}
