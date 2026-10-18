package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnEnvironmentLeaseCoversEveryStackOfThatEnvironmentAndNoOther(t *testing.T) {
	t.Parallel()

	lease := provider.Lease{Tier: environment.TierPreview, Project: "shop", Env: "pr-7"}
	for name, want := range map[provider.StackRef]bool{
		{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}:                                        true,
		{Project: "shop", Tier: environment.TierPreview, Name: naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))}: true,
		{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-8")}:                                        false,
		{Project: "blog", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}:                                        false,
		{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("pr-7")}:                                     false,
	} {
		if got := lease.Covers(name); got != want {
			t.Errorf("Covers(%+v) = %t, want %t", name, got, want)
		}
	}
}

func TestAProjectLeaseCoversEveryEnvironmentOfThatProject(t *testing.T) {
	t.Parallel()

	lease := provider.Lease{Tier: environment.TierPreview, Project: "shop"}
	if !lease.Covers(provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-9")}) {
		t.Error("a project lease does not cover one of its environments")
	}
	if lease.Covers(provider.StackRef{Project: "blog", Tier: environment.TierPreview, Name: naming.InfraStack("pr-9")}) {
		t.Error("a project lease covers another project")
	}
}

func TestNoLeaseCoversAnyStack(t *testing.T) {
	t.Parallel()

	if (provider.Lease{}).Covers(provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}) {
		t.Error("the zero lease covers a stack, so a run holding no lease would break another run's lock")
	}
}
