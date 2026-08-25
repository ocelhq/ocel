package providerkit_test

import (
	"context"
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func fronting(t *testing.T, kind edge.Kind) (providerkit.Gate, *fake.Provider) {
	t.Helper()

	gate, provider := gated(t, providerkit.Writer("1.0.0"))
	gate.Edge = kind
	return gate, provider
}

func planFeatures(t *testing.T, gate providerkit.Gate, req providerkit.ApplyRequest) []string {
	t.Helper()

	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var featured []string
	for _, group := range plan.Groups {
		if group.Feature != "" && group.Action != providerkit.ActionDelete {
			featured = append(featured, group.Feature)
		}
	}
	return featured
}

func TestABootstrapEnsuresTheFeatureItsEdgeNeeds(t *testing.T) {
	t.Parallel()

	gate, _ := fronting(t, fake.KindRelay)
	featured := planFeatures(t, gate, providerkit.ApplyRequest{})
	if !slices.Contains(featured, fake.FeatureImages) {
		t.Fatalf("a plain bootstrap behind the %s edge plans %v, want the feature that edge needs", fake.KindRelay, featured)
	}
	if !slices.Contains(featured, fake.FeatureCache) {
		t.Errorf("plan = %v, want what the edge's feature depends on pulled in with it", featured)
	}
}

func TestABootstrapBehindAnotherEdgeEnsuresNothing(t *testing.T) {
	t.Parallel()

	gate, _ := fronting(t, fake.KindDirect)
	if featured := planFeatures(t, gate, providerkit.ApplyRequest{}); len(featured) != 0 {
		t.Fatalf("a plain bootstrap behind the %s edge plans %v, want nothing: no feature names that edge", fake.KindDirect, featured)
	}
}

func TestAStandingEdgeFeatureLeftOutIsDroppedNotReinstated(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindRelay)
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var dropped []string
	for _, group := range plan.Groups {
		if group.Action == providerkit.ActionDelete {
			dropped = append(dropped, group.Feature)
		}
	}
	if !slices.Contains(dropped, fake.FeatureImages) {
		t.Fatalf("plan drops %v, want the standing edge feature this run asked to leave out", dropped)
	}
}

func TestAnUnrequestedStandingEdgeFeatureIsKept(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindDirect)
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	for _, group := range plan.Groups {
		if group.Feature == fake.FeatureImages && group.Action == providerkit.ActionDelete {
			t.Fatalf("bootstrapping behind %s deletes %s, want another edge's standing feature untouched", fake.KindDirect, fake.FeatureImages)
		}
	}
}

func TestDroppingAnEdgeFeatureNamesTheProjectsBehindIt(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindDirect)
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	recordProject(t, provider, "shop", fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureCache},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var reason string
	for _, group := range plan.Groups {
		if group.Feature == fake.FeatureImages && group.Action == providerkit.ActionDelete {
			reason = group.Reason
		}
	}
	if reason == "" {
		t.Fatalf("dropping %s says nothing about the projects deployed against it", fake.FeatureImages)
	}
	if err := gate.Apply(context.Background(), providerkit.ClassProduction, providerkit.ApplyRequest{
		Features: []string{fake.FeatureCache},
	}, nil); err == nil {
		t.Fatal("dropping an edge feature a deployed project needs was admitted, want it refused")
	}
}

func TestARunNamingNoEdgeStillNeedsTheDefaultEdgesFeature(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight(): %v", err)
	}
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetFeature() != fake.FeatureImages {
			continue
		}
		if !stack.GetRequired() || !stack.GetPresent() {
			t.Fatalf("%s = %+v, want the default edge's feature required and standing after a bootstrap that named no edge", fake.FeatureImages, stack)
		}
		return
	}
	t.Fatalf("Preflight() reported no %s stack at all", fake.FeatureImages)
}
