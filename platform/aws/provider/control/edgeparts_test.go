package control

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type describingEdge struct {
	teardownEdge

	parts []edge.BootstrapPart
	err   error
	tiers []environment.Tier
}

func (e *describingEdge) Hooks() edge.Hooks {
	return edge.Hooks{
		DescribeBootstrap: func(_ context.Context, tier environment.Tier) ([]edge.BootstrapPart, error) {
			e.tiers = append(e.tiers, tier)
			return e.parts, e.err
		},
	}
}

func bootstrapperWithEdge(t *testing.T, front edge.Edge) Bootstrap {
	t.Helper()

	b := installedBootstrapper(t, environment.TierProduction)
	stacks := b.CFN.(*teardownCFN)
	core, err := defaultNamespace.StackNameFor(environment.TierProduction)
	if err != nil {
		t.Fatalf("StackNameFor: %v", err)
	}
	stacks.present[defaultNamespace.PartStackName(bootstrap.CloudflareEdgePart, environment.TierProduction)] = stacks.present[core]
	b.Edge, b.Edges, b.Kinds = front, registryOf(front), kindsOf(front)
	return b
}

func TestAnAWSBootstrapStatusListsTheEdgesBootstrapPartsUnderItsPart(t *testing.T) {
	t.Parallel()

	front := &describingEdge{parts: []edge.BootstrapPart{
		{Name: "ocel-releases-store/BOOTSTRAP_SECRET", Current: true},
		{Name: "ocel-sample-worker", Current: false},
	}}
	b := bootstrapperWithEdge(t, front)

	described, err := b.Describe(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	want := []provider.BootstrapStack{
		{Name: "cloudflare/ocel-releases-store/BOOTSTRAP_SECRET", Part: bootstrap.CloudflareEdgePart, Present: true, DigestCurrent: true},
		{Name: "cloudflare/ocel-sample-worker", Part: bootstrap.CloudflareEdgePart, Present: true, DigestCurrent: false},
	}
	var got []provider.BootstrapStack
	for _, stack := range described.Stacks {
		if strings.HasPrefix(stack.Name, string(cloudflareKind)+"/") {
			got = append(got, stack)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edge stacks = %+v, want %+v", got, want)
	}
	if len(front.tiers) != 1 || front.tiers[0] != environment.TierProduction {
		t.Errorf("the edge described its bootstrap for %v, want the tier asked about", front.tiers)
	}
}

func TestAnAWSBootstrapStatusReportsAnUndescribableEdgeAsUnreadableRatherThanFailingOrStale(t *testing.T) {
	t.Parallel()

	b := bootstrapperWithEdge(t, &describingEdge{err: errors.New("CLOUDFLARE_API_TOKEN is not set")})

	described, err := b.Describe(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Describe = %v, want the status to survive an edge it cannot read", err)
	}
	want := provider.BootstrapStack{Name: "cloudflare/bootstrap", Part: bootstrap.CloudflareEdgePart, Present: true, ReadError: "CLOUDFLARE_API_TOKEN is not set"}
	var got []provider.BootstrapStack
	for _, stack := range described.Stacks {
		if strings.HasPrefix(stack.Name, string(cloudflareKind)+"/") {
			got = append(got, stack)
		}
	}
	if !reflect.DeepEqual(got, []provider.BootstrapStack{want}) {
		t.Errorf("edge stacks = %+v, want [%+v]", got, want)
	}
}

func TestAnAWSBootstrapPlanIsUnchangedByTheEdgesBootstrapParts(t *testing.T) {
	t.Parallel()

	planned := []edge.PlanChange{{Kind: "Cloudflare::Worker", Name: "ocel-sample-worker", Action: edge.PlanKeep}}
	req := provider.BootstrapRequest{
		Tier:  environment.TierProduction,
		Parts: []string{bootstrap.CloudflareEdgePart},
	}
	planWith := func(front edge.Edge) provider.Plan {
		t.Helper()
		plan, err := planningBootstrapper(front).Plan(context.Background(), req)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		return plan
	}

	without := planWith(&planningEdge{planned: planned})
	with := planWith(&planningDescribingEdge{
		planningEdge: planningEdge{planned: planned},
		parts:        []edge.BootstrapPart{{Name: "ocel-sample-worker", Current: false}},
	})
	if !reflect.DeepEqual(with, without) {
		t.Errorf("plan with the edge's parts = %+v, want the plan without them %+v", with, without)
	}
}

type planningDescribingEdge struct {
	planningEdge

	parts []edge.BootstrapPart
}

func (e *planningDescribingEdge) Hooks() edge.Hooks {
	hooks := e.planningEdge.Hooks()
	hooks.DescribeBootstrap = func(context.Context, environment.Tier) ([]edge.BootstrapPart, error) {
		return e.parts, nil
	}
	return hooks
}
