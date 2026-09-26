package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func fronting(t *testing.T, kind edge.Kind) (providerserver.Gate, *fake.Provider) {
	t.Helper()

	gate, p := gated(t, provider.WrittenBy("1.0.0"))
	gate.Edge = kind
	return gate, p
}

func planFeatures(t *testing.T, gate providerserver.Gate, req providerserver.ApplyRequest) []string {
	t.Helper()

	plan, err := gate.Plan(context.Background(), edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var featured []string
	for _, group := range plan.Groups {
		if group.Feature != "" && group.Action != provider.ActionDelete {
			featured = append(featured, group.Feature)
		}
	}
	return featured
}

func TestABootstrapEnsuresTheFeatureItsEdgeNeeds(t *testing.T) {
	t.Parallel()

	gate, _ := fronting(t, fake.KindRelay)
	featured := planFeatures(t, gate, providerserver.ApplyRequest{})
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
	if featured := planFeatures(t, gate, providerserver.ApplyRequest{}); len(featured) != 0 {
		t.Fatalf("a plain bootstrap behind the %s edge plans %v, want nothing: no feature names that edge", fake.KindDirect, featured)
	}
}

func TestAnInstalledEdgeFeatureLeftOutIsKept(t *testing.T) {
	t.Parallel()

	gate, p := fronting(t, fake.KindRelay)
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), edge.ClassProduction, providerserver.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	for _, group := range plan.Groups {
		if group.Action == provider.ActionDelete {
			t.Fatalf("plan deletes %s, want a run that named no removal to take nothing down", group.Feature)
		}
	}
}

func TestAnEdgeFeatureGoesOnlyWhenTheRunNamesIt(t *testing.T) {
	t.Parallel()

	gate, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), edge.ClassProduction, providerserver.ApplyRequest{
		Remove: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var removed []string
	for _, group := range plan.Groups {
		if group.Action == provider.ActionDelete {
			removed = append(removed, group.Feature)
		}
	}
	if !slices.Equal(removed, []string{fake.FeatureImages}) {
		t.Fatalf("plan deletes %v, want exactly the feature the run named", removed)
	}
}

func TestAnUnrequestedInstalledEdgeFeatureIsKept(t *testing.T) {
	t.Parallel()

	gate, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	plan, err := gate.Plan(context.Background(), edge.ClassProduction, providerserver.ApplyRequest{
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	for _, group := range plan.Groups {
		if group.Feature == fake.FeatureImages && group.Action == provider.ActionDelete {
			t.Fatalf("bootstrapping behind %s deletes %s, want another edge's installed feature untouched", fake.KindDirect, fake.FeatureImages)
		}
	}
}

func TestRemovingAnEdgeFeatureNamesTheProjectsBehindIt(t *testing.T) {
	t.Parallel()

	gate, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)
	recordProject(t, p, "shop", fake.FeatureImages)

	req := providerserver.ApplyRequest{Remove: []string{fake.FeatureImages}}
	plan, err := gate.Plan(context.Background(), edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var reason string
	for _, group := range plan.Groups {
		if group.Feature == fake.FeatureImages && group.Action == provider.ActionDelete {
			reason = group.Reason
		}
	}
	if reason == "" {
		t.Fatalf("removing %s says nothing about the projects deployed against it", fake.FeatureImages)
	}
	if err := gate.Apply(context.Background(), plan, edge.ClassProduction, req, nil); err == nil {
		t.Fatal("removing an edge feature a deployed project needs was admitted, want it refused")
	}
}

func TestRemovingTheFeatureTheChosenEdgeFrontsThroughIsRefused(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindRelay)
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	_, err := gate.Plan(context.Background(), edge.ClassProduction, providerserver.ApplyRequest{
		Remove: []string{fake.FeatureImages},
	})
	if err == nil {
		t.Fatalf("a run behind the %s edge removed the feature that edge fronts through", fake.KindRelay)
	}
	if !strings.Contains(err.Error(), fake.FeatureImages+" fronts this project's deploys") {
		t.Errorf("refusal = %q, want it to name the edge this project fronts with", err)
	}
	if strings.Contains(err.Error(), "either ensured or removed") {
		t.Errorf("refusal = %q, want the real conflict rather than features the run never asked to install", err)
	}
}

func TestRemovingWhatTheFrontingFeatureDependsOnIsRefusedByName(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindRelay)
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	_, err := gate.Plan(context.Background(), edge.ClassProduction, providerserver.ApplyRequest{
		Remove: []string{fake.FeatureCache},
	})
	if err == nil {
		t.Fatalf("removing %s took %s, the feature the %s edge fronts through, with it", fake.FeatureCache, fake.FeatureImages, fake.KindRelay)
	}
	if !strings.Contains(err.Error(), "removing "+fake.FeatureCache+" takes "+fake.FeatureImages+" down with it") {
		t.Errorf("refusal = %q, want it to name what the cascade would take", err)
	}
	if strings.Contains(err.Error(), "either ensured or removed") {
		t.Errorf("refusal = %q, want the real conflict rather than features the run never asked to install", err)
	}
}

func TestRemovingAnEdgeFeatureThatFrontsNothingHereGoesAhead(t *testing.T) {
	t.Parallel()

	gate, provider := fronting(t, fake.KindDirect)
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache, fake.FeatureImages)

	ctx := context.Background()
	req := providerserver.ApplyRequest{Remove: []string{fake.FeatureImages}}
	plan, err := gate.Plan(ctx, edge.ClassProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if err := gate.Apply(ctx, plan, edge.ClassProduction, req, nil); err != nil {
		t.Fatalf("removing an edge feature no project here fronts with: %v", err)
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
			t.Fatalf("%s = %+v, want the default edge's feature required and installed after a bootstrap that named no edge", fake.FeatureImages, stack)
		}
		return
	}
	t.Fatalf("Preflight() reported no %s stack at all", fake.FeatureImages)
}
