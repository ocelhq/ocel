package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func RunRetirement(t *testing.T, hooks *provider.RetirementHooks) {
	t.Helper()

	ctx := context.Background()
	if hooks.Plan == nil || hooks.Retire == nil {
		t.Fatal("Retirement leaves Plan or Retire nil, and a deploy retires only what its dry run showed")
	}
	untouched := provider.LiveApps{Slug: "conformance-untouched", Tier: environment.TierPreview, Env: "pr-0", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless}}}

	t.Run("an environment nothing was deployed to plans nothing", func(t *testing.T) {
		plan, err := hooks.Plan(ctx, untouched, progress.Discard())
		if err != nil {
			t.Fatalf("Plan() = %v", err)
		}
		if len(plan.Groups) != 0 {
			t.Errorf("Plan() = %v, want nothing to retire where nothing was deployed", plan.Groups)
		}
	})
	t.Run("retiring twice is a no-op the second time", func(t *testing.T) {
		gone := untouched
		gone.Apps = nil
		for range 2 {
			if err := hooks.Retire(ctx, gone, progress.Discard()); err != nil {
				t.Fatalf("Retire() = %v, want a removed environment's rerun to pass over what is already gone", err)
			}
		}
		plan, err := hooks.Plan(ctx, gone, progress.Discard())
		if err != nil {
			t.Fatalf("Plan() after Retire() = %v", err)
		}
		if len(plan.Groups) != 0 {
			t.Errorf("Plan() after Retire() = %v, want nothing left to retire", plan.Groups)
		}
	})
}
