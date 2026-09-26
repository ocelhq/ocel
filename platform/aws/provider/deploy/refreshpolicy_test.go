package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestOnlyADestroyOfAStackThisProcessDidNotRealizeRefreshes(t *testing.T) {
	realized := &Realized{}
	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	policy := refreshPolicy(realized)

	for _, op := range []kitpulumi.Op{kitpulumi.OpProvision} {
		if policy(ref, op) {
			t.Errorf("refresh before %v = true, want false: a refresh reads every resource in the stack back from the provider, and an update that finds one gone fails on its own", op)
		}
	}
	if !policy(ref, kitpulumi.OpDestroy) {
		t.Error("refresh before a destroy of a stack another process realized = false, want true: a destroy is not tolerant of resources already gone")
	}

	realized.mark("shop", ref.Name)
	if policy(ref, kitpulumi.OpDestroy) {
		t.Error("refresh before a destroy of a stack this process just realized = true, want false: its state is what this process wrote")
	}

	t.Setenv(skipTeardownRefreshEnv, "1")
	other := provider.StackRef{Project: "blog", Class: edge.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	if policy(other, kitpulumi.OpDestroy) {
		t.Errorf("refresh before a destroy with %s set = true, want false: the harness sets it to trade the drift check for speed", skipTeardownRefreshEnv)
	}
}
