package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestTheFakeBootstrapOffersAndPlansWhatATestGivesIt(t *testing.T) {
	t.Parallel()

	b := fake.NewBootstrap()
	b.Offers(provider.Feature{Name: "search"})
	if catalogue := b.Catalogue(); len(catalogue) != 1 || catalogue[0].Name != "search" {
		t.Errorf("catalogue = %v, want the one feature offered", catalogue)
	}

	given := provider.Plan{Groups: []provider.ChangeGroup{{Kind: provider.StackGroupKind, Name: "fake-production", Action: provider.ActionUpdate}}}
	b.PlansWith(given)
	plan, err := b.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction})
	if err != nil || len(plan.Groups) != 1 || plan.Groups[0].Action != provider.ActionUpdate {
		t.Errorf("Plan = %+v, %v, want the plan given", plan, err)
	}

	refused := errors.New("the edge account is not set")
	b.RefusePlan(refused)
	if _, err := b.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}); !errors.Is(err, refused) {
		t.Errorf("Plan err = %v, want the refusal", err)
	}

	b.PlansRemovalWith(given)
	removal, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil || len(removal.Groups) != 1 {
		t.Errorf("PlanRemove = %+v, %v, want the removal plan given though nothing is applied", removal, err)
	}
}
