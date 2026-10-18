package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func fronting(t *testing.T, kind edge.Kind) (bootstrapplan.Readiness, *fake.Provider) {
	t.Helper()

	readiness, p := newReadiness(t, provider.WrittenBy("1.0.0"))
	readiness.Edge = kind
	return readiness, p
}

func planParts(t *testing.T, readiness bootstrapplan.Readiness, req bootstrapplan.ApplyRequest) []string {
	t.Helper()

	plan, err := readiness.Plan(context.Background(), environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var planned []string
	for _, group := range plan.Groups {
		if group.Part != "" && group.Action != provider.ActionDelete {
			planned = append(planned, group.Part)
		}
	}
	return planned
}

func TestABootstrapEnsuresThePartItsEdgeRequires(t *testing.T) {
	t.Parallel()

	readiness, _ := fronting(t, fake.KindRelay)
	planned := planParts(t, readiness, bootstrapplan.ApplyRequest{})
	if !slices.Contains(planned, fake.PartImages) {
		t.Fatalf("a plain bootstrap behind the %s edge plans %v, want the part that edge requires", fake.KindRelay, planned)
	}
}

func TestABootstrapBehindAnotherEdgeEnsuresNothing(t *testing.T) {
	t.Parallel()

	readiness, _ := fronting(t, fake.KindDirect)
	if planned := planParts(t, readiness, bootstrapplan.ApplyRequest{}); len(planned) != 0 {
		t.Fatalf("a plain bootstrap behind the %s edge plans %v, want nothing: no part names that edge", fake.KindDirect, planned)
	}
}

func TestAnInstalledEdgePartLeftOutIsKept(t *testing.T) {
	t.Parallel()

	readiness, p := fronting(t, fake.KindRelay)
	bootstrapped(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)

	plan, err := readiness.Plan(context.Background(), environment.TierProduction, bootstrapplan.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	for _, group := range plan.Groups {
		if group.Action == provider.ActionDelete {
			t.Fatalf("plan deletes %s, want a run that named no removal to take nothing down", group.Part)
		}
	}
}

func TestAnEdgePartGoesOnlyWhenTheRunNamesIt(t *testing.T) {
	t.Parallel()

	readiness, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)

	plan, err := readiness.Plan(context.Background(), environment.TierProduction, bootstrapplan.ApplyRequest{
		Remove: []string{fake.PartImages},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var removed []string
	for _, group := range plan.Groups {
		if group.Action == provider.ActionDelete {
			removed = append(removed, group.Part)
		}
	}
	if !slices.Equal(removed, []string{fake.PartImages}) {
		t.Fatalf("plan deletes %v, want exactly the part the run named", removed)
	}
}

func TestAnUnrequestedInstalledEdgePartIsKept(t *testing.T) {
	t.Parallel()

	readiness, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)

	plan, err := readiness.Plan(context.Background(), environment.TierProduction, bootstrapplan.ApplyRequest{})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	for _, group := range plan.Groups {
		if group.Part == fake.PartImages && group.Action == provider.ActionDelete {
			t.Fatalf("bootstrapping behind %s deletes %s, want another edge's installed part untouched", fake.KindDirect, fake.PartImages)
		}
	}
}

func TestRemovingAnEdgePartNamesTheProjectsBehindIt(t *testing.T) {
	t.Parallel()

	readiness, p := fronting(t, fake.KindDirect)
	bootstrapped(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)
	recordProject(t, p, "shop", fake.PartImages)

	req := bootstrapplan.ApplyRequest{Remove: []string{fake.PartImages}, Force: true}
	plan, err := readiness.Plan(context.Background(), environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	var reason string
	for _, group := range plan.Groups {
		if group.Part == fake.PartImages && group.Action == provider.ActionDelete {
			reason = group.Reason
		}
	}
	if !strings.Contains(reason, "shop") {
		t.Fatalf("removing %s reads %q, want it to name the project that uses it", fake.PartImages, reason)
	}
}

func TestRemovingThePartTheChosenEdgeFrontsThroughIsRefused(t *testing.T) {
	t.Parallel()

	readiness, vendor := fronting(t, fake.KindRelay)
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache, fake.PartImages)

	_, err := readiness.Plan(context.Background(), environment.TierProduction, bootstrapplan.ApplyRequest{
		Remove: []string{fake.PartImages},
	})
	if err == nil {
		t.Fatalf("a run behind the %s edge removed the part that edge fronts through", fake.KindRelay)
	}
	if !strings.Contains(err.Error(), fake.PartImages+" fronts this project's deploys") {
		t.Errorf("refusal = %q, want it to name the edge this project fronts with", err)
	}
	if strings.Contains(err.Error(), "either ensured or removed") {
		t.Errorf("refusal = %q, want the real conflict rather than parts the run never asked to install", err)
	}
}

func TestRemovingAnEdgePartThatFrontsNothingHereGoesAhead(t *testing.T) {
	t.Parallel()

	readiness, vendor := fronting(t, fake.KindDirect)
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache, fake.PartImages)

	ctx := context.Background()
	req := bootstrapplan.ApplyRequest{Remove: []string{fake.PartImages}}
	plan, err := readiness.Plan(ctx, environment.TierProduction, req)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if err := readiness.Apply(ctx, plan, environment.TierProduction, req, nil); err != nil {
		t.Fatalf("removing an edge part no project here fronts with: %v", err)
	}
}

func TestARunNamingNoEdgeStillNeedsTheDefaultEdgesPart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight(): %v", err)
	}
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetPart() != fake.PartImages {
			continue
		}
		if !stack.GetRequired() || !stack.GetPresent() {
			t.Fatalf("%s = %+v, want the default edge's part required and installed after a bootstrap that named no edge", fake.PartImages, stack)
		}
		return
	}
	t.Fatalf("Preflight() reported no %s stack at all", fake.PartImages)
}
