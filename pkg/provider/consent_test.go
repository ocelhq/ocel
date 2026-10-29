package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	if err := provider.RefuseUnconsentedChanges(shown, shrunk); err != nil {
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
	err := provider.RefuseUnconsentedChanges(shown, grown)
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

	err := provider.RefuseUnconsentedChanges(shown, grown)
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
		if err := provider.RefuseUnconsentedChanges(plan(shown), plan(current)); err != nil {
			t.Errorf("RefuseUnconsentedChanges() from %s to %s = %v, want the apply to run", shown, current, err)
		}
	}
	if err := provider.RefuseUnconsentedChanges(plan(provider.ActionAdopt), plan(provider.ActionCreate)); err == nil {
		t.Error("RefuseUnconsentedChanges() let an install through where the plan showed the engine adopted, and nobody consented to one")
	}
}

func TestAConsentedChangeThatBecameADeleteOrReplaceIsWorkNobodyConsentedTo(t *testing.T) {
	t.Parallel()

	plan := func(action provider.ChangeAction) provider.Plan {
		return provider.Plan{Groups: []provider.ChangeGroup{{
			Kind:    provider.StackGroupKind,
			Name:    "core",
			Changes: []provider.Change{{Kind: "bucket", Name: "state", Action: action}},
		}}}
	}
	for _, moved := range [][2]provider.ChangeAction{
		{provider.ActionUpdate, provider.ActionDelete},
		{provider.ActionUpdate, provider.ActionReplace},
		{provider.ActionCreate, provider.ActionReplace},
		{provider.ActionReplace, provider.ActionDelete},
		{provider.ActionDisableThenDelete, provider.ActionDelete},
	} {
		err := provider.RefuseUnconsentedChanges(plan(moved[0]), plan(moved[1]))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("RefuseUnconsentedChanges() from %s to %s = %v, want an invalid refusal: nobody consented to the %s", moved[0], moved[1], err, moved[1])
			continue
		}
		if !strings.Contains(refused.Message, "state") || !strings.Contains(refused.Message, string(moved[1])) {
			t.Errorf("the refusal reads %q, want it to name the row and what it became", refused.Message)
		}
	}
	for _, kept := range [][2]provider.ChangeAction{
		{provider.ActionDelete, provider.ActionDelete},
		{provider.ActionReplace, provider.ActionReplace},
		{provider.ActionReplace, provider.ActionUpdate},
		{provider.ActionDelete, provider.ActionKeep},
	} {
		if err := provider.RefuseUnconsentedChanges(plan(kept[0]), plan(kept[1])); err != nil {
			t.Errorf("RefuseUnconsentedChanges() from %s to %s = %v, want the apply to run: it does no more than the plan showed", kept[0], kept[1], err)
		}
	}
}

func TestAResourceShownTwiceInOneGroupKeepsTheConsentItsWritingRowGot(t *testing.T) {
	t.Parallel()

	keep := provider.Change{Kind: "oidc-provider", Name: "console.example.com/oidc", Action: provider.ActionKeep}
	update := provider.Change{Kind: "oidc-provider", Name: "console.example.com/oidc", Action: provider.ActionUpdate}
	for _, rows := range [][]provider.Change{{keep, update}, {update, keep}} {
		plan := provider.Plan{Groups: []provider.ChangeGroup{{Kind: provider.StackGroupKind, Name: "console-trust", Changes: rows}}}
		if err := provider.RefuseUnconsentedChanges(plan, plan); err != nil {
			t.Errorf("RefuseUnconsentedChanges() over the plan %+v it was shown = %v, want the apply to run", rows, err)
		}
	}
}
