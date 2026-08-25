package providerkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func groupFor(t *testing.T, plan providerkit.BootstrapPlan, feature string) providerkit.ChangeGroup {
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

func TestPlanShowsADropItRefusesToApply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	recordProject(t, provider, "shop", fake.FeatureImages)

	req := providerkit.ApplyRequest{Features: []string{fake.FeatureCache}}
	plan, err := gate.Plan(ctx, providerkit.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan() error = %v, want a plan that shows the drop rather than refusing it", err)
	}
	dropped := groupFor(t, plan, fake.FeatureImages)
	if dropped.Action != providerkit.ActionDelete {
		t.Errorf("the images group is %q, want it deleted where it left the set", dropped.Action)
	}
	if !strings.Contains(dropped.Reason, "shop") {
		t.Errorf("the images group reads %q, want it to name the project deployed against it", dropped.Reason)
	}
	if err := gate.Apply(ctx, providerkit.ClassProduction, req, nil); err == nil {
		t.Error("Apply() took the drop the plan warned about without --force")
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
		Drop:     []string{fake.FeatureImages},
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
