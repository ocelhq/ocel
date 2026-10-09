package provider_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnEnvironmentLeaseCoversEveryStackOfThatEnvironmentAndNoOther(t *testing.T) {
	t.Parallel()

	leased := provider.WithLease(context.Background(), provider.Lease{Tier: environment.TierPreview, Project: "shop", Env: "pr-7"})
	for name, want := range map[provider.StackRef]bool{
		{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}:                                        true,
		{Project: "shop", Tier: environment.TierPreview, Name: naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))}: true,
		{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-8")}:                                        false,
		{Project: "blog", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}:                                        false,
		{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("pr-7")}:                                     false,
	} {
		if got := provider.HasLease(leased, name); got != want {
			t.Errorf("HasLease(%+v) = %t, want %t", name, got, want)
		}
	}
}

func TestAProjectLeaseCoversEveryEnvironmentOfThatProject(t *testing.T) {
	t.Parallel()

	leased := provider.WithLease(context.Background(), provider.Lease{Tier: environment.TierPreview, Project: "shop"})
	if !provider.HasLease(leased, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-9")}) {
		t.Error("a project lease does not cover one of the project's environments")
	}
	if provider.HasLease(leased, provider.StackRef{Project: "blog", Tier: environment.TierPreview, Name: naming.InfraStack("pr-9")}) {
		t.Error("a project lease covers another project's stack")
	}
}

func TestALeaseTakenInsideAnotherKeepsTheOuterOneCovering(t *testing.T) {
	t.Parallel()

	outer := provider.WithLease(context.Background(), provider.Lease{Tier: environment.TierPreview, Project: "shop"})
	inner := provider.WithLease(outer, provider.Lease{Tier: environment.TierPreview, Project: "shop", Env: "pr-7"})
	if !provider.HasLease(inner, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-8")}) {
		t.Error("an environment lease taken under a project lease hides the project lease")
	}
}

func TestALeaseWhoseContextEndedCoversNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	leased := provider.WithLease(ctx, provider.Lease{Tier: environment.TierPreview, Project: "shop", Env: "pr-7"})
	cancel()
	if provider.HasLease(leased, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}) {
		t.Error("a lease covers a stack after the context it was held under ended")
	}
}

func TestNoLeaseCoversNothing(t *testing.T) {
	t.Parallel()

	if provider.HasLease(context.Background(), provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}) {
		t.Error("a context with no lease covers a stack")
	}
}
