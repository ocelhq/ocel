package providerserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestApplyRefusesWorkThatAppearedAfterThePlanWasDrawn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, p := newReadiness(t, "1.2.3")
	bootstrapped(t, p, environment.TierProduction, fake.PartImages)

	req := providerserver.ApplyRequest{}
	shown, err := readiness.Plan(ctx, environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, shown, fake.PartImages).Action; action != provider.ActionKeep {
		t.Fatalf("the images group is %q, want the plan to show no change on it", action)
	}

	p.FakeBootstrap().MarkStale(fake.PartImages)

	err = readiness.Apply(ctx, shown, environment.TierProduction, req, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Apply() over work that appeared after the plan was drawn = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, fake.PartImages) {
		t.Errorf("the refusal reads %q, want it to name what moved under the plan", refused.Message)
	}
}

func TestApplyRunsThePlanItWasShown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "1.2.3")

	req := providerserver.ApplyRequest{}
	shown, err := readiness.Plan(ctx, environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := readiness.Apply(ctx, shown, environment.TierProduction, req, nil); err != nil {
		t.Fatalf("Apply() of the plan it was shown = %v, want it applied", err)
	}
	if len(vendor.FakeBootstrap().Applied()) == 0 {
		t.Error("Apply() provisioned nothing for the plan it was shown")
	}
}

func groupFor(t *testing.T, plan provider.Plan, part string) provider.ChangeGroup {
	t.Helper()

	for _, group := range plan.Groups {
		if group.Part == part {
			return group
		}
	}
	t.Fatalf("Plan() has no group for %q; it has %v", part, plan.Groups)
	return provider.ChangeGroup{}
}

func TestPlanOnAFreshAccountCreatesTheBaselineAndEveryPart(t *testing.T) {
	t.Parallel()

	readiness, _ := newReadiness(t, "1.2.3")
	plan, err := readiness.Plan(context.Background(), environment.TierProduction, providerserver.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("Plan() = %v, want the baseline and images, the part the relay edge requires", plan.Groups)
	}
	for _, group := range plan.Groups {
		if group.Action != provider.ActionCreate {
			t.Errorf("Plan() has %s at %q, want it created on an empty account", group.Name, group.Action)
		}
		if group.Kind != provider.StackGroupKind || group.Name == "" {
			t.Errorf("Plan() returned %+v, and a plan renders a kind and a name", group)
		}
	}
	if reason := groupFor(t, plan, fake.PartImages).Reason; reason != "" {
		t.Errorf("the images group reads %q, want a create to say nothing the sigil has not already said", reason)
	}
}

func TestPlanSeparatesTheStaleFromTheCurrent(t *testing.T) {
	t.Parallel()

	readiness, p := newReadiness(t, "1.2.3")
	bootstrapped(t, p, environment.TierProduction, fake.PartImages)
	p.FakeBootstrap().MarkStale(fake.PartImages)

	plan, err := readiness.Plan(context.Background(), environment.TierProduction, providerserver.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, plan, "").Action; action != provider.ActionKeep {
		t.Errorf("the base group is %q, want it kept where nothing about it moved", action)
	}
	stale := groupFor(t, plan, fake.PartImages)
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
	readiness, p := newReadiness(t, "1.2.3")
	readiness.Edge = fake.KindDirect
	bootstrapped(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)
	recordProject(t, p, "shop", fake.PartImages)

	req := providerserver.ApplyRequest{Remove: []string{fake.PartImages}}
	plan, err := readiness.Plan(ctx, environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v, want a plan that shows the removal rather than refusing it", err)
	}
	removed := groupFor(t, plan, fake.PartImages)
	if removed.Action != provider.ActionDelete {
		t.Errorf("the images group is %q, want it deleted where the run named it for removal", removed.Action)
	}
	if !strings.Contains(removed.Reason, "shop") {
		t.Errorf("the images group reads %q, want it to name the project deployed against it", removed.Reason)
	}
	if err := readiness.Apply(ctx, plan, environment.TierProduction, req, nil); err == nil {
		t.Error("Apply() took the removal the plan warned about without --force")
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
	read, err := providerserver.PlanFromProto(providerserver.ChangePlanProto(shown, "production", ""))
	if err != nil {
		t.Fatalf("PlanFromProto() over a plan that adopts = %v, want it read back", err)
	}
	if got := read.Groups[0].Changes[0].Action; got != provider.ActionAdopt {
		t.Errorf("an adopted row reads back as %q, want %q", got, provider.ActionAdopt)
	}
}
