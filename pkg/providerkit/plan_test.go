package providerkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestApplyRefusesWorkThatAppearedAfterThePlanWasDrawn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, p := gated(t, "1.2.3")
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache)

	req := providerkit.ApplyRequest{Features: []string{fake.FeatureCache}}
	shown, err := gate.Plan(ctx, edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, shown, fake.FeatureCache).Action; action != provider.ActionKeep {
		t.Fatalf("the cache group is %q, want the plan to show nothing owed on it", action)
	}

	p.FakeBootstrap().Behind(fake.FeatureCache)

	err = gate.Apply(ctx, shown, edge.ClassProduction, req, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Apply() over work that appeared after the plan was drawn = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, fake.FeatureCache) {
		t.Errorf("the refusal reads %q, want it to name what moved under the plan", refused.Message)
	}
}

func TestApplyRunsThePlanItWasShown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")

	req := providerkit.ApplyRequest{Features: []string{fake.FeatureCache}}
	shown, err := gate.Plan(ctx, edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := gate.Apply(ctx, shown, edge.ClassProduction, req, nil); err != nil {
		t.Fatalf("Apply() of the plan it was shown = %v, want it applied", err)
	}
	if len(provider.FakeBootstrap().Applied()) == 0 {
		t.Error("Apply() stood nothing up for the plan it was shown")
	}
}

func groupFor(t *testing.T, plan provider.Plan, feature string) provider.ChangeGroup {
	t.Helper()

	for _, group := range plan.Groups {
		if group.Feature == feature {
			return group
		}
	}
	t.Fatalf("Plan() carries no group for %q; it carries %v", feature, plan.Groups)
	return provider.ChangeGroup{}
}

func TestPlanOnAFreshAccountCreatesTheBaselineAndEveryFeature(t *testing.T) {
	t.Parallel()

	gate, _ := gated(t, "1.2.3")
	plan, err := gate.Plan(context.Background(), edge.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Groups) != 3 {
		t.Fatalf("Plan() = %v, want the baseline and the closure of images", plan.Groups)
	}
	for _, group := range plan.Groups {
		if group.Action != provider.ActionCreate {
			t.Errorf("Plan() has %s at %q, want it created on an account holding nothing", group.Name, group.Action)
		}
		if group.Kind != provider.StackGroupKind || group.Name == "" {
			t.Errorf("Plan() returned %+v, and a plan renders a kind and a name", group)
		}
	}
	if reason := groupFor(t, plan, fake.FeatureCache).Reason; reason != "" {
		t.Errorf("the cache group reads %q, want a create to say nothing the sigil has not already said", reason)
	}
}

func TestPlanSeparatesTheStaleFromTheCurrent(t *testing.T) {
	t.Parallel()

	gate, p := gated(t, "1.2.3")
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	p.FakeBootstrap().Behind(fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), edge.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, plan, fake.FeatureCache).Action; action != provider.ActionKeep {
		t.Errorf("the cache group is %q, want it kept where nothing about it moved", action)
	}
	stale := groupFor(t, plan, fake.FeatureImages)
	if stale.Action != provider.ActionUpdate {
		t.Errorf("the images group is %q, want it updated where its content is behind", stale.Action)
	}
	if stale.Reason == "" && len(stale.Changes) == 0 {
		t.Error("the images group is an update with neither children nor a reason, which reads as no change at all")
	}
}

func TestPlanShowsARemovalItRefusesToApply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, p := gated(t, "1.2.3")
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	recordProject(t, p, "shop", fake.FeatureImages)

	req := providerkit.ApplyRequest{Remove: []string{fake.FeatureImages}}
	plan, err := gate.Plan(ctx, edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v, want a plan that shows the removal rather than refusing it", err)
	}
	removed := groupFor(t, plan, fake.FeatureImages)
	if removed.Action != provider.ActionDelete {
		t.Errorf("the images group is %q, want it deleted where the run named it for removal", removed.Action)
	}
	if !strings.Contains(removed.Reason, "shop") {
		t.Errorf("the images group reads %q, want it to name the project deployed against it", removed.Reason)
	}
	if err := gate.Apply(ctx, plan, edge.ClassProduction, req, nil); err == nil {
		t.Error("Apply() took the removal the plan warned about without --force")
	}
}

func TestPlanLeavesAStandingFeatureNoRunNamed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(ctx, edge.ClassProduction, providerkit.ApplyRequest{Features: []string{fake.FeatureCache}})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	for _, group := range plan.Groups {
		if group.Feature == fake.FeatureImages {
			t.Fatalf("a run naming only %s plans %+v for %s, want a feature it never mentioned left out of the plan entirely",
				fake.FeatureCache, group, fake.FeatureImages)
		}
	}
}

func TestPlanRefusesToEnsureAndRemoveTheSameFeature(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)

	_, err := gate.Plan(context.Background(), edge.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureCache},
		Remove:   []string{fake.FeatureCache},
	})
	if err == nil {
		t.Fatal("Plan() took a run that both stands a feature up and takes it down")
	}
	if !strings.Contains(err.Error(), fake.FeatureCache) {
		t.Errorf("err = %v, want it to name the feature asked for both ways", err)
	}
}

func TestAnAdoptedRowCrossesTheWireAsItself(t *testing.T) {
	t.Parallel()

	if !provider.ValidChangeAction(provider.ActionAdopt) {
		t.Fatal("ValidChangeAction(adopt) = false, and a plan that adopts what it finds fails every conformance check")
	}
	shown := provider.Plan{Groups: []provider.ChangeGroup{{
		Kind:   provider.StackGroupKind,
		Name:   "core",
		Action: provider.ActionKeep,
		Changes: []provider.Change{
			{Kind: "docker:engine", Name: "docker", Action: provider.ActionAdopt, Reason: "docker 28.3.1, not managed by ocel: upgrading it is yours"},
		},
	}}}
	read, err := providerkit.PlanOf(providerkit.ChangePlanProto(shown, "production", ""))
	if err != nil {
		t.Fatalf("PlanOf() over a plan that adopts = %v, want it read back", err)
	}
	if got := read.Groups[0].Changes[0].Action; got != provider.ActionAdopt {
		t.Errorf("an adopted row reads back as %q, want %q", got, provider.ActionAdopt)
	}
}
