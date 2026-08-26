package providerkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestAnApplyMayShrinkThePlanItShowedAndNeverGrowIt(t *testing.T) {
	t.Parallel()

	shown := providerkit.Plan{Groups: []providerkit.ChangeGroup{{
		Kind: providerkit.StackGroupKind,
		Name: "core",
		Changes: []providerkit.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: providerkit.ActionCreate},
			{Kind: "unit", Name: "docker", Action: providerkit.ActionKeep},
		},
	}}}

	shrunk := providerkit.Plan{Groups: []providerkit.ChangeGroup{{
		Kind: providerkit.StackGroupKind,
		Name: "core",
		Changes: []providerkit.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: providerkit.ActionKeep},
			{Kind: "unit", Name: "docker", Action: providerkit.ActionKeep},
		},
	}}}
	if err := providerkit.RefuseGrowth(shown, shrunk); err != nil {
		t.Fatalf("RefuseGrowth() over a plan that only shrank = %v, want the apply to run", err)
	}

	grown := providerkit.Plan{Groups: []providerkit.ChangeGroup{{
		Kind: providerkit.StackGroupKind,
		Name: "core",
		Changes: []providerkit.Change{
			{Kind: "dir", Name: "/etc/ocel", Action: providerkit.ActionCreate},
			{Kind: "unit", Name: "docker", Action: providerkit.ActionCreate},
		},
	}}}
	err := providerkit.RefuseGrowth(shown, grown)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("RefuseGrowth() over work the plan never showed = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, "docker") {
		t.Errorf("the refusal reads %q, want it to name the row that moved", refusal.Message)
	}
}

func TestAGroupTheShownPlanNeverCarriedIsWorkNobodyConsentedTo(t *testing.T) {
	t.Parallel()

	shown := providerkit.Plan{Groups: []providerkit.ChangeGroup{
		{Kind: providerkit.StackGroupKind, Name: "core", Action: providerkit.ActionKeep},
	}}
	grown := providerkit.Plan{Groups: []providerkit.ChangeGroup{
		{Kind: providerkit.StackGroupKind, Name: "core", Action: providerkit.ActionKeep},
		{Kind: providerkit.StackGroupKind, Name: "cache-stack", Action: providerkit.ActionCreate},
	}}

	err := providerkit.RefuseGrowth(shown, grown)
	if err == nil {
		t.Fatal("RefuseGrowth() let a group the plan never showed through, and consent was attached to the plan")
	}
	if !strings.Contains(err.Error(), "cache-stack") {
		t.Errorf("the refusal reads %q, want it to name the group that appeared", err)
	}
}

func TestApplyRefusesWorkThatAppearedAfterThePlanWasDrawn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	req := providerkit.ApplyRequest{Features: []string{fake.FeatureCache}}
	shown, err := gate.Plan(ctx, providerkit.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, shown, fake.FeatureCache).Action; action != providerkit.ActionKeep {
		t.Fatalf("the cache group is %q, want the plan to show nothing owed on it", action)
	}

	provider.Bootstrapper().Behind(fake.FeatureCache)

	err = gate.Apply(ctx, shown, providerkit.ClassProduction, req, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Apply() over work that appeared after the plan was drawn = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, fake.FeatureCache) {
		t.Errorf("the refusal reads %q, want it to name what moved under the plan", refusal.Message)
	}
}

func TestApplyRunsThePlanItWasShown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")

	req := providerkit.ApplyRequest{Features: []string{fake.FeatureCache}}
	shown, err := gate.Plan(ctx, providerkit.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := gate.Apply(ctx, shown, providerkit.ClassProduction, req, nil); err != nil {
		t.Fatalf("Apply() of the plan it was shown = %v, want it applied", err)
	}
	if len(provider.Bootstrapper().Applied()) == 0 {
		t.Error("Apply() stood nothing up for the plan it was shown")
	}
}

func groupFor(t *testing.T, plan providerkit.Plan, feature string) providerkit.ChangeGroup {
	t.Helper()

	for _, group := range plan.Groups {
		if group.Feature == feature {
			return group
		}
	}
	t.Fatalf("Plan() carries no group for %q; it carries %v", feature, plan.Groups)
	return providerkit.ChangeGroup{}
}

func TestPlanOnAFreshAccountCreatesTheBaselineAndEveryFeature(t *testing.T) {
	t.Parallel()

	gate, _ := gated(t, "1.2.3")
	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Groups) != 3 {
		t.Fatalf("Plan() = %v, want the baseline and the closure of images", plan.Groups)
	}
	for _, group := range plan.Groups {
		if group.Action != providerkit.ActionCreate {
			t.Errorf("Plan() has %s at %q, want it created on an account holding nothing", group.Name, group.Action)
		}
		if group.Kind != providerkit.StackGroupKind || group.Name == "" {
			t.Errorf("Plan() returned %+v, and a plan renders a kind and a name", group)
		}
	}
	if reason := groupFor(t, plan, fake.FeatureCache).Reason; reason != "" {
		t.Errorf("the cache group reads %q, want a create to say nothing the sigil has not already said", reason)
	}
}

func TestPlanSeparatesTheStaleFromTheCurrent(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	provider.Bootstrapper().Behind(fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if action := groupFor(t, plan, fake.FeatureCache).Action; action != providerkit.ActionKeep {
		t.Errorf("the cache group is %q, want it kept where nothing about it moved", action)
	}
	stale := groupFor(t, plan, fake.FeatureImages)
	if stale.Action != providerkit.ActionUpdate {
		t.Errorf("the images group is %q, want it updated where its content is behind", stale.Action)
	}
	if stale.Reason == "" && len(stale.Changes) == 0 {
		t.Error("the images group is an update with neither children nor a reason, which reads as no change at all")
	}
}

func TestPlanShowsARemovalItRefusesToApply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	recordProject(t, provider, "shop", fake.FeatureImages)

	req := providerkit.ApplyRequest{Remove: []string{fake.FeatureImages}}
	plan, err := gate.Plan(ctx, providerkit.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v, want a plan that shows the removal rather than refusing it", err)
	}
	removed := groupFor(t, plan, fake.FeatureImages)
	if removed.Action != providerkit.ActionDelete {
		t.Errorf("the images group is %q, want it deleted where the run named it for removal", removed.Action)
	}
	if !strings.Contains(removed.Reason, "shop") {
		t.Errorf("the images group reads %q, want it to name the project deployed against it", removed.Reason)
	}
	if err := gate.Apply(ctx, plan, providerkit.ClassProduction, req, nil); err == nil {
		t.Error("Apply() took the removal the plan warned about without --force")
	}
}

func TestPlanLeavesAStandingFeatureNoRunNamed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(ctx, providerkit.ClassProduction, providerkit.ApplyRequest{Features: []string{fake.FeatureCache}})
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
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	_, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
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

func TestEdgeGroupCarriesTheEdgesOwnKindsAndRollsTheirActionsUp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		planned []edge.PlanChange
		action  providerkit.ChangeAction
		reason  string
	}{
		{
			name: "nothing stands",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanCreate},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanCreate},
			},
			action: providerkit.ActionCreate,
		},
		{
			name: "everything stands",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep, Reason: "already current"},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanKeep, Reason: "already current"},
			},
			action: providerkit.ActionKeep,
			reason: "already current",
		},
		{
			name: "one has drifted",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanUpdate, Reason: "the deployed script differs"},
			},
			action: providerkit.ActionUpdate,
		},
		{
			name: "one is missing",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanCreate},
			},
			action: providerkit.ActionUpdate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			group, err := providerkit.EdgeGroup("cloudflare", "cloudflare-edge", tc.planned)
			if err != nil {
				t.Fatalf("EdgeGroup() error = %v", err)
			}
			if group.Kind != providerkit.EdgeGroupKind || group.Name != "cloudflare/edge" {
				t.Errorf("group = %+v, want the cloudflare edge named under its own vendor", group)
			}
			if group.Feature != "cloudflare-edge" {
				t.Errorf("group feature = %q, want the feature the edge participates through", group.Feature)
			}
			if group.Action != tc.action || group.Reason != tc.reason {
				t.Errorf("group action = %q (%q), want %q (%q)", group.Action, group.Reason, tc.action, tc.reason)
			}
			if len(group.Changes) != len(tc.planned) {
				t.Fatalf("group carries %d changes, want one per planned change", len(group.Changes))
			}
			for i, change := range group.Changes {
				if change.Kind != tc.planned[i].Kind || change.Name != tc.planned[i].Name {
					t.Errorf("change %d = %+v, want the edge's own kind and name verbatim", i, change)
				}
				if !providerkit.ValidChangeAction(change.Action) {
					t.Errorf("change %d is %q, which no renderer knows", i, change.Action)
				}
			}
		})
	}
}

func TestEdgeGroupRefusesAnActionNoRendererKnows(t *testing.T) {
	t.Parallel()

	_, err := providerkit.EdgeGroup("cloudflare", "cloudflare-edge", []edge.PlanChange{
		{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanAction("recreate")},
	})
	if err == nil {
		t.Fatal("EdgeGroup() took an action no renderer knows, which the CLI draws as a bare ?")
	}
	for _, want := range []string{"recreate", "ocel-isr-writer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %q", err, want)
		}
	}
}

func TestEdgeGroupThatAccountsForNothingIsNotCalledCurrent(t *testing.T) {
	t.Parallel()

	group, err := providerkit.EdgeGroup("cloudflare", "cloudflare-edge", nil)
	if err != nil {
		t.Fatalf("EdgeGroup() error = %v", err)
	}
	if group.Action == providerkit.ActionKeep {
		t.Errorf("group = %+v, want an edge that listed no resource not to claim it is current", group)
	}
	if group.Reason != providerkit.DetailUnavailable {
		t.Errorf("group reason = %q, want %q", group.Reason, providerkit.DetailUnavailable)
	}
}

func TestEdgeGroupOf(t *testing.T) {
	t.Parallel()

	t.Run("a group carrying nothing is a group all the same", func(t *testing.T) {
		t.Parallel()

		converted, err := providerkit.EdgeGroupOf(edge.PlanGroup{
			Kind:   providerkit.EdgeGroupKind,
			Name:   "cloudflare/edge",
			Action: edge.PlanKeep,
			Reason: "bootstrap-scoped",
		})
		if err != nil {
			t.Fatalf("EdgeGroupOf() error = %v", err)
		}
		if converted.Action != providerkit.ActionKeep || converted.Reason != "bootstrap-scoped" {
			t.Errorf("group = %+v, want the kept group and the reason it is kept for", converted)
		}
		if len(converted.Changes) != 0 {
			t.Errorf("group carries %+v, want the rows it was given: none", converted.Changes)
		}
	})

	t.Run("an unnamed action is refused rather than left in place", func(t *testing.T) {
		t.Parallel()

		_, err := providerkit.EdgeGroupOf(edge.PlanGroup{})
		if err == nil {
			t.Fatal("EdgeGroupOf() took a group naming no action, which reads as kept while the removal deletes it")
		}
	})

	t.Run("a group whose rows name an action no renderer knows is refused", func(t *testing.T) {
		t.Parallel()

		_, err := providerkit.EdgeGroupOf(edge.PlanGroup{
			Kind:   providerkit.EdgeGroupKind,
			Name:   "cloudflare/edge",
			Action: edge.PlanDelete,
			Changes: []edge.PlanChange{
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanAction("recreate")},
			},
		})
		if err == nil {
			t.Fatal("EdgeGroupOf() took a row action no renderer knows")
		}
		for _, want := range []string{"recreate", "ocel-isr-writer"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %v does not name %q", err, want)
			}
		}
	})
}

func TestVendoredNamesEveryGroupUnderTheVendorThatHoldsIt(t *testing.T) {
	t.Parallel()

	groups := []providerkit.ChangeGroup{{Kind: providerkit.StackGroupKind, Name: "ocel-bootstrap"}}
	named := providerkit.Vendored("aws", groups)
	if named[0].Name != "aws/ocel-bootstrap" {
		t.Errorf("group name = %q, want the stack under its vendor", named[0].Name)
	}
	if groups[0].Name != "ocel-bootstrap" {
		t.Error("Vendored() renamed the groups it was handed rather than returning named ones")
	}
}

func TestFeatureNeedingEdgeFindsTheFeatureTheEdgeParticipatesThrough(t *testing.T) {
	t.Parallel()

	catalogue := []providerkit.Feature{
		{Name: "isr"},
		{Name: "cloudflare-edge", Needs: []string{providerkit.NeedsEdgePrefix + "cloudflare"}},
	}
	if got := providerkit.FeatureNeedingEdge(catalogue, "cloudflare"); got != "cloudflare-edge" {
		t.Errorf("FeatureNeedingEdge(cloudflare) = %q, want the feature that names it", got)
	}
	if got := providerkit.FeatureNeedingEdge(catalogue, "cloudfront"); got != "" {
		t.Errorf("FeatureNeedingEdge(cloudfront) = %q, want nothing where no feature names it", got)
	}
}

func TestDeriveGroupsNamesTheStacksTheVendorDescribed(t *testing.T) {
	t.Parallel()

	described := providerkit.Bootstrap{
		Class:   providerkit.ClassPreview,
		Present: true,
		Stacks: []providerkit.BootstrapStack{
			{Name: "core", Present: true, Schema: providerkit.BootstrapSchema, DigestCurrent: true},
			{Name: "cache-stack", Feature: fake.FeatureCache, Present: true, Schema: providerkit.BootstrapSchema},
		},
	}
	groups := providerkit.DeriveGroups(described, fake.NewBootstrapper().Catalogue(), providerkit.BootstrapRequest{
		Class:    providerkit.ClassPreview,
		Features: []string{fake.FeatureCache},
		Remove:   []string{fake.FeatureImages},
	})
	if len(groups) != 3 {
		t.Fatalf("DeriveGroups() = %v, want the baseline, the kept feature and the dropped one", groups)
	}
	if groups[0].Name != "core" || groups[0].Action != providerkit.ActionKeep {
		t.Errorf("the baseline group = %+v, want core kept", groups[0])
	}
	if groups[1].Name != "cache-stack" || groups[1].Action != providerkit.ActionUpdate {
		t.Errorf("the cache group = %+v, want the stale stack updated", groups[1])
	}
	if groups[2].Action != providerkit.ActionDelete || groups[2].Feature != fake.FeatureImages {
		t.Errorf("the images group = %+v, want the dropped feature deleted", groups[2])
	}
}
