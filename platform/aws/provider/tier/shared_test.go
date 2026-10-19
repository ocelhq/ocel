package tier

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func sharedPartRemovingInto(removed *[]environment.Tier, present bool) Part {
	return Shared(SharedSpec{
		Part: "containers",
		Describe: func(context.Context, environment.Tier) (provider.BootstrapPart, bool, error) {
			return provider.BootstrapPart{Name: "containers"}, present, nil
		},
		Remove: func(_ context.Context, tier environment.Tier, _ progress.Log) error {
			*removed = append(*removed, tier)
			return nil
		},
	})
}

func sharedRemovals(part Part) map[string]*Removal {
	return map[string]*Removal{"a bootstrap teardown": part.Teardown, "a removal of the part": part.Removal}
}

func TestASharedPartNoDeployRaisedIsLeftUntouchedByEveryRemoval(t *testing.T) {
	t.Parallel()

	var removed []environment.Tier
	for name, removal := range sharedRemovals(sharedPartRemovingInto(&removed, false)) {
		if err := removal.Remove(context.Background(), environment.TierProduction, nil); err != nil {
			t.Fatalf("%s = %v, want nothing to do for a part the tier never raised", name, err)
		}
	}
	if len(removed) != 0 {
		t.Errorf("the shared part was removed for %v though the tier never raised it, and its removal opens a stack in a state bucket the teardown has to delete", removed)
	}
}

func TestASharedPartADeployRaisedIsTakenDownByEveryRemoval(t *testing.T) {
	t.Parallel()

	var removed []environment.Tier
	for name, removal := range sharedRemovals(sharedPartRemovingInto(&removed, true)) {
		removed = nil
		if err := removal.Remove(context.Background(), environment.TierProduction, nil); err != nil {
			t.Fatalf("%s = %v", name, err)
		}
		if !slices.Equal(removed, []environment.Tier{environment.TierProduction}) {
			t.Errorf("%s removed the shared part for %v, want the production tier once", name, removed)
		}
	}
}
