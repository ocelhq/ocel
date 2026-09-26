package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestAGroupThatAdoptsAndKeepsRollsUpAsKept(t *testing.T) {
	t.Parallel()

	action, reason := provider.RollUp([]provider.Change{
		{Kind: "dir", Name: "/etc/ocel", Action: provider.ActionKeep},
		{Kind: "docker:engine", Name: "docker", Action: provider.ActionAdopt},
	})
	if action != provider.ActionKeep || reason == "" {
		t.Errorf("RollUp() over a keep and an adopt = %q %q, want it kept: adopting writes nothing", action, reason)
	}
	if action, _ := provider.RollUp([]provider.Change{
		{Kind: "dir", Name: "/etc/ocel", Action: provider.ActionCreate},
		{Kind: "docker:engine", Name: "docker", Action: provider.ActionAdopt},
	}); action != provider.ActionUpdate {
		t.Errorf("RollUp() over a create and an adopt = %q, want %q: what is adopted already stands, as what is kept does", action, provider.ActionUpdate)
	}
}
