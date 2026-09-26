package bootstrapplan_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestAnApplyMayShrinkThePlanItShowedAndNeverGrowIt(t *testing.T) {
	t.Parallel()

	shown := provider.Plan{Groups: []provider.ChangeGroup{{
		Kind: provider.StackGroupKind,
		Name: "core",
		Changes: []provider.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: provider.ActionCreate},
			{Kind: "unit", Name: "docker", Action: provider.ActionKeep},
		},
	}}}

	shrunk := provider.Plan{Groups: []provider.ChangeGroup{{
		Kind: provider.StackGroupKind,
		Name: "core",
		Changes: []provider.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: provider.ActionKeep},
			{Kind: "unit", Name: "docker", Action: provider.ActionKeep},
		},
	}}}
	if err := bootstrapplan.RefuseUnconsentedChanges(shown, shrunk); err != nil {
		t.Fatalf("RefuseUnconsentedChanges() over a plan that only shrank = %v, want the apply to run", err)
	}

	grown := provider.Plan{Groups: []provider.ChangeGroup{{
		Kind: provider.StackGroupKind,
		Name: "core",
		Changes: []provider.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: provider.ActionCreate},
			{Kind: "unit", Name: "docker", Action: provider.ActionCreate},
		},
	}}}
	err := bootstrapplan.RefuseUnconsentedChanges(shown, grown)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("RefuseUnconsentedChanges() over work the plan never showed = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, "docker") {
		t.Errorf("the refusal reads %q, want it to name the row that moved", refused.Message)
	}
}

func TestAGroupTheShownPlanNeverIncludedIsWorkNobodyConsentedTo(t *testing.T) {
	t.Parallel()

	shown := provider.Plan{Groups: []provider.ChangeGroup{
		{Kind: provider.StackGroupKind, Name: "core", Action: provider.ActionKeep},
	}}
	grown := provider.Plan{Groups: []provider.ChangeGroup{
		{Kind: provider.StackGroupKind, Name: "core", Action: provider.ActionKeep},
		{Kind: provider.StackGroupKind, Name: "cache-stack", Action: provider.ActionCreate},
	}}

	err := bootstrapplan.RefuseUnconsentedChanges(shown, grown)
	if err == nil {
		t.Fatal("RefuseUnconsentedChanges() let a group the plan never showed through, and consent was attached to the plan")
	}
	if !strings.Contains(err.Error(), "cache-stack") {
		t.Errorf("the refusal reads %q, want it to name the group that appeared", err)
	}
}

func TestAnAdoptionWritesNothingAndSoNeverGrowsThePlan(t *testing.T) {
	t.Parallel()

	plan := func(action provider.ChangeAction) provider.Plan {
		return provider.Plan{Groups: []provider.ChangeGroup{{
			Kind:    provider.StackGroupKind,
			Name:    "core",
			Changes: []provider.Change{{Kind: "docker:engine", Name: "docker", Action: action}},
		}}}
	}
	for shown, current := range map[provider.ChangeAction]provider.ChangeAction{
		provider.ActionAdopt: provider.ActionAdopt,
		provider.ActionKeep:  provider.ActionAdopt,
	} {
		if err := bootstrapplan.RefuseUnconsentedChanges(plan(shown), plan(current)); err != nil {
			t.Errorf("RefuseUnconsentedChanges() from %s to %s = %v, want the apply to run", shown, current, err)
		}
	}
	if err := bootstrapplan.RefuseUnconsentedChanges(plan(provider.ActionAdopt), plan(provider.ActionCreate)); err == nil {
		t.Error("RefuseUnconsentedChanges() let an install through where the plan showed the engine adopted, and nobody consented to one")
	}
}
