package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func itemFor(t *testing.T, items []*deploymentsv1.TeardownItem, kind, name string) *deploymentsv1.TeardownItem {
	t.Helper()
	for _, item := range items {
		if item.GetKind() == kind && (name == "" || item.GetName() == name) {
			return item
		}
	}
	t.Fatalf("no %q %q item in %v", kind, name, itemLines(items))
	return nil
}

func itemLines(items []*deploymentsv1.TeardownItem) []string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.GetAction().String()+" "+item.GetKind()+" "+item.GetName())
	}
	return lines
}

func productionState(t *testing.T, recorded bootstrap.Production) edge.StackState {
	t.Helper()
	state, err := bootstrap.WithProduction(edge.StackState{edge.StackKeySlug: "shop"}, recorded)
	if err != nil {
		t.Fatalf("WithProduction: %v", err)
	}
	return state
}

func TestEdgeStackPlan(t *testing.T) {
	t.Parallel()

	planFor := func(t *testing.T, state edge.StackState) *deploymentsv1.EdgeStackPlan {
		t.Helper()
		s := &Server{}
		edgeFront, err := s.edge(edge.KindCloudflare, "eu-west-1")
		if err != nil {
			t.Fatalf("edge() error = %v", err)
		}
		plan, err := s.edgeStackPlan(edgeFront, projectPlanScope{
			kind:       edgeFront.Kind(),
			class:      bootstrap.ClassProduction,
			slug:       "shop",
			stateTable: "ocel-state",
			stacks:     1,
			state:      state,
		})
		if err != nil {
			t.Fatalf("edgeStackPlan() error = %v", err)
		}
		return plan
	}

	t.Run("a project with no production deploy is stamped with the edge and plans nothing", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, nil)
		if plan.GetEdgeKind() != string(edge.KindCloudflare) {
			t.Errorf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if len(plan.GetItems()) != 0 {
			t.Errorf("items = %v, want none for a project that never deployed", plan.GetItems())
		}
	})

	t.Run("an empty stack state is stamped with the edge and plans nothing", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.StackState{})
		if plan.GetEdgeKind() != string(edge.KindCloudflare) {
			t.Errorf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if len(plan.GetItems()) != 0 {
			t.Errorf("items = %v, want none for an empty stack state", plan.GetItems())
		}
	})

	t.Run("a deployed stack plans the workers, the store and the shared substrate it leaves", func(t *testing.T) {
		t.Parallel()

		items := planFor(t, edge.StackState{"instance": "shop-abc"}).GetItems()
		if got := itemFor(t, items, "edge workers", "shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("edge workers action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "deployments store", "shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("deployments store action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "tag clock rows", "shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("tag clock rows action = %v, want DELETE", got)
		}
		table := itemFor(t, items, "state table", "ocel-state")
		if table.GetAction() != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("state table action = %v, want KEEP", table.GetAction())
		}
		if !strings.Contains(table.GetReason(), "ocel bootstrap --destroy") {
			t.Errorf("state table reason = %q, want it to name what does remove it", table.GetReason())
		}
	})
}

func TestDestroyPlanItemsPerEdge(t *testing.T) {
	t.Parallel()

	t.Run("the native edge disables its distribution before deleting it", func(t *testing.T) {
		t.Parallel()

		state := edge.RecordBoundDomain(edge.StackState{edge.StackKeyFront: "d123.cloudfront.net"}, "shop.example.com")
		items, err := destroyPlanItems(projectPlanScope{
			kind: edge.KindNative, class: bootstrap.ClassProduction, slug: "shop", state: state,
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}
		dist := itemFor(t, items, "distribution", "d123.cloudfront.net")
		if dist.GetAction() != deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE || !dist.GetSlow() {
			t.Errorf("distribution = %v (slow=%v), want DISABLE_THEN_DELETE and slow", dist.GetAction(), dist.GetSlow())
		}
		if got := itemFor(t, items, "edge routes", "shop.example.com").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("edge routes action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "deployments ledger", "production/shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("deployments ledger action = %v, want DELETE", got)
		}
	})

	t.Run("with no edge bought the APIs and their domain names go", func(t *testing.T) {
		t.Parallel()

		state := edge.RecordBoundDomain(edge.StackState{}, "shop.example.com")
		items, err := destroyPlanItems(projectPlanScope{
			kind: edge.KindNone, class: bootstrap.ClassProduction, slug: "shop", state: state,
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}
		apis := itemFor(t, items, "REST APIs", "shop")
		if apis.GetAction() != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("REST APIs action = %v, want DELETE", apis.GetAction())
		}
		if !apis.GetSlow() {
			t.Error("REST APIs slow = false: API Gateway paces their deletion to one every 30 seconds, and the plan is where that is said")
		}
		if got := itemFor(t, items, "domain names", "shop.example.com").GetSlow(); got {
			t.Error("domain names slow = true, want only the quota-paced items marked slow")
		}
		if got := itemFor(t, items, "domain names", "shop.example.com").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("domain names action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "preview fallback API", "").GetAction(); got != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("preview fallback API action = %v, want KEEP", got)
		}
	})

	t.Run("a pinned certificate and a record ocel never wrote are kept with a reason", func(t *testing.T) {
		t.Parallel()

		recorded := bootstrap.Production{
			Certificate: certs.Certificate{ARN: "arn:aws:acm:eu-west-1:1:certificate/ocel"},
			Written:     []edge.Record{{Name: "_acme.shop.example.com", Type: edge.RecordTypeCNAME, Value: "validate.acm-validations.aws"}},
			Owed:        []edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}},
			Hosts: []bootstrap.Provisioned{{
				Hostname:    "pinned.example.com",
				Certificate: "arn:aws:acm:eu-west-1:1:certificate/pinned",
			}},
		}
		items, err := destroyPlanItems(projectPlanScope{
			kind: edge.KindNative, class: bootstrap.ClassProduction, slug: "shop", state: productionState(t, recorded),
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}

		if got := itemFor(t, items, "certificate", "arn:aws:acm:eu-west-1:1:certificate/ocel").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("ocel-requested certificate action = %v, want DELETE", got)
		}
		pinned := itemFor(t, items, "certificate", "arn:aws:acm:eu-west-1:1:certificate/pinned")
		if pinned.GetAction() != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("pinned certificate action = %v, want KEEP", pinned.GetAction())
		}
		if !strings.Contains(pinned.GetReason(), "pinned for pinned.example.com") {
			t.Errorf("pinned certificate reason = %q, want it to say whose pin it is", pinned.GetReason())
		}
		owed := itemFor(t, items, "DNS record", "shop.example.com CNAME d123.cloudfront.net")
		if owed.GetAction() != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("owed record action = %v, want KEEP", owed.GetAction())
		}
		if !strings.Contains(owed.GetReason(), "ocel never wrote it") {
			t.Errorf("owed record reason = %q, want it to say why it stays", owed.GetReason())
		}
		if got := itemFor(t, items, "DNS record", "_acme.shop.example.com CNAME validate.acm-validations.aws").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
	})

	t.Run("an adopted certificate is never ocel's to delete", func(t *testing.T) {
		t.Parallel()

		recorded := bootstrap.Production{Certificate: certs.Certificate{ARN: "arn:adopted", Adopted: true}}
		items, err := destroyPlanItems(projectPlanScope{
			kind: edge.KindNative, class: bootstrap.ClassProduction, slug: "shop", state: productionState(t, recorded),
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}
		if got := itemFor(t, items, "certificate", "arn:adopted").GetAction(); got != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("adopted certificate action = %v, want KEEP", got)
		}
	})

	t.Run("the records ocel wrote for the stack itself are planned too", func(t *testing.T) {
		t.Parallel()

		written, err := json.Marshal([]edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}})
		if err != nil {
			t.Fatalf("marshal records: %v", err)
		}
		items, err := destroyPlanItems(projectPlanScope{
			kind:  edge.KindNative,
			class: bootstrap.ClassProduction,
			slug:  "shop",
			state: edge.StackState{edge.StackKeyRecords: string(written)},
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}
		if got := itemFor(t, items, "DNS record", "shop.example.com CNAME d123.cloudfront.net").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
	})

	t.Run("a project served on the shared preview wildcard keeps it", func(t *testing.T) {
		t.Parallel()

		items, err := destroyPlanItems(projectPlanScope{
			kind:  edge.KindNone,
			class: bootstrap.ClassPreview,
			slug:  "shop",
			state: edge.StackState{edge.StackKeyGlobalPreview: "preview.acme.com"},
		})
		if err != nil {
			t.Fatalf("destroyPlanItems: %v", err)
		}
		wildcard := itemFor(t, items, "preview wildcard", "*.preview.acme.com")
		if wildcard.GetAction() != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("preview wildcard action = %v, want KEEP", wildcard.GetAction())
		}
		if !strings.Contains(wildcard.GetReason(), "ocel domain release --preview") {
			t.Errorf("preview wildcard reason = %q, want it to name what releases it", wildcard.GetReason())
		}
	})
}

func TestPlanDestroyProjectUnsupportedEdge(t *testing.T) {
	t.Parallel()

	client := newTestClientFor(t, &Server{}, testToken)
	_, err := client.PlanDestroyProject(context.Background(), &deploymentsv1.PlanDestroyProjectRequest{Slug: "shop", EdgeKind: "bogus"})
	if err == nil {
		t.Fatal("PlanDestroyProject error = nil, want the unsupported edge refused")
	}
	for _, want := range []string{"bogus", string(edge.KindCloudflare)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestPlanDestroyProjectWithoutAnEdgeKind(t *testing.T) {
	t.Parallel()

	client := newTestClientFor(t, &Server{}, testToken)
	_, err := client.PlanDestroyProject(context.Background(), &deploymentsv1.PlanDestroyProjectRequest{Slug: "shop"})
	if err == nil {
		t.Fatal("PlanDestroyProject error = nil, want a request that names no edge refused rather than defaulted to one")
	}
	for _, want := range edge.KindNames(edge.AllKinds()) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q as an edge the CLI may send", err, want)
		}
	}
}

func TestDestroyPlanFollowsTheEdgeTheRequestNames(t *testing.T) {
	t.Parallel()

	state := edge.RecordBoundDomain(edge.StackState{}, "shop.example.com")

	planFor := func(t *testing.T, kind edge.Kind) *deploymentsv1.EdgeStackPlan {
		t.Helper()
		s := &Server{}
		edgeFront, err := s.edge(kind, "eu-west-1")
		if err != nil {
			t.Fatalf("edge(%q) error = %v", kind, err)
		}
		plan, err := s.edgeStackPlan(edgeFront, projectPlanScope{
			kind:       edgeFront.Kind(),
			class:      bootstrap.ClassProduction,
			slug:       "shop",
			stateTable: "ocel-state",
			stacks:     1,
			state:      state,
		})
		if err != nil {
			t.Fatalf("edgeStackPlan() error = %v", err)
		}
		return plan
	}

	t.Run("a project that bought no edge plans its APIs, not Cloudflare workers", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.KindNone)
		if plan.GetEdgeKind() != string(edge.KindNone) {
			t.Fatalf("edge_kind = %q, want none: an `edge: false` project must not be planned as a Cloudflare teardown", plan.GetEdgeKind())
		}
		if got := itemFor(t, plan.GetItems(), "REST APIs", "shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("REST APIs action = %v, want DELETE", got)
		}
		for _, item := range plan.GetItems() {
			if item.GetKind() == "edge workers" {
				t.Errorf("plan = %v, want no Cloudflare workers in a no-edge teardown", itemLines(plan.GetItems()))
			}
		}
	})

	t.Run("a project fronted by Cloudflare plans its workers", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.KindCloudflare)
		if plan.GetEdgeKind() != string(edge.KindCloudflare) {
			t.Fatalf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if got := itemFor(t, plan.GetItems(), "edge workers", "shop").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("edge workers action = %v, want DELETE", got)
		}
	})
}

func TestForgetStackStateOnlyOnceTheTeardownFinished(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	recorded := edge.StackState{edge.StackKeySlug: "shop", edge.StackKeyEndpoint: "https://store.example"}

	standing := func(t *testing.T, ssmc *stateSSM) edge.StackState {
		t.Helper()
		state, err := bootstrap.ReadStackStateFor(ctx, ssmc, bootstrap.ClassProduction, "shop")
		if err != nil {
			t.Fatalf("ReadStackStateFor: %v", err)
		}
		return state
	}
	written := func(t *testing.T) *stateSSM {
		t.Helper()
		ssmc := &stateSSM{params: map[string]string{}}
		if err := bootstrap.WriteStackStateFor(ctx, ssmc, bootstrap.ClassProduction, "shop", recorded); err != nil {
			t.Fatalf("WriteStackStateFor: %v", err)
		}
		return ssmc
	}

	t.Run("a failed edge destroy leaves the state the rerun reads its progress from", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: false}
		if err := forgetStackState(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, errors.New("the front is on fire")); err != nil {
			t.Fatalf("forgetStackState: %v", err)
		}
		if got := standing(t, ssmc); len(got) == 0 {
			t.Fatal("the stack state was forgotten after a failed teardown: the rerun has nothing to resume from")
		}
	})

	t.Run("a torn-down edge whose later steps failed still leaves the state standing", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: true}
		if err := forgetStackState(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, errors.New("the asset bucket is gone")); err != nil {
			t.Fatalf("forgetStackState: %v", err)
		}
		if got := standing(t, ssmc); len(got) == 0 {
			t.Fatal("the stack state was forgotten while the purge that failed still has work left")
		}
	})

	t.Run("a clean teardown forgets it last", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: true}
		if err := forgetStackState(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, nil); err != nil {
			t.Fatalf("forgetStackState: %v", err)
		}
		if got := standing(t, ssmc); len(got) != 0 {
			t.Fatalf("stack state = %v after a finished teardown, want nothing left to resume", got)
		}
	})
}
