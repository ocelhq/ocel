package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
)

func TestOnlyADestroyOfAStackThisProcessDidNotRealizeRefreshes(t *testing.T) {
	realized := &Realized{}
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	policy := (&Stacks{realized: realized}).refreshPolicy()

	for _, op := range []pulumi.Operation{pulumi.OperationProvision} {
		if policy(ref, op) {
			t.Errorf("refresh before %v = true, want false: a refresh reads every resource in the stack back from the provider, and an update that finds one gone fails on its own", op)
		}
	}
	if !policy(ref, pulumi.OperationDestroy) {
		t.Error("refresh before a destroy of a stack another process realized = false, want true: a destroy is not tolerant of resources already gone")
	}

	realized.mark("shop", ref.Name)
	if policy(ref, pulumi.OperationDestroy) {
		t.Error("refresh before a destroy of a stack this process just realized = true, want false: its state is what this process wrote")
	}

	other := provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	skipping := (&Stacks{realized: realized, skipChecks: true}).refreshPolicy()
	if skipping(other, pulumi.OperationDestroy) {
		t.Error("refresh before a destroy with the checks skipped = true, want false: the harness skips them to trade the drift check for speed")
	}
}
