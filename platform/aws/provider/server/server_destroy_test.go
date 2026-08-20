package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func itemFor(t *testing.T, items []*contractv1.RemovalItem, kind, name string) *contractv1.RemovalItem {
	t.Helper()
	for _, item := range items {
		if item.GetKind() == kind && (name == "" || item.GetName() == name) {
			return item
		}
	}
	t.Fatalf("no %q %q item in %v", kind, name, itemLines(items))
	return nil
}

func itemLines(items []*contractv1.RemovalItem) []string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.GetAction().String()+" "+item.GetKind()+" "+item.GetName())
	}
	return lines
}

func productionRecord(recorded domains.Settlement) bootstrap.StackRecord {
	return bootstrap.StackRecord{Edge: edge.StackState{Slug: "shop"}, Production: recorded}
}

func TestEdgeStackPlan(t *testing.T) {
	t.Parallel()

	planFor := func(t *testing.T, state edge.StackState) *contractv1.EdgeStackPlan {
		t.Helper()
		s := &Server{}
		edgeFront, err := s.edge(cloudflare.Kind, "eu-west-1")
		if err != nil {
			t.Fatalf("edge() error = %v", err)
		}
		plan, err := s.edgeStackPlan(edgeFront, projectPlanScope{
			class:      bootstrap.ClassProduction,
			slug:       "shop",
			stateTable: "ocel-state",
			stacks:     1,
			record:     bootstrap.StackRecord{Edge: state},
		})
		if err != nil {
			t.Fatalf("edgeStackPlan() error = %v", err)
		}
		return plan
	}

	t.Run("a project with no production deploy is stamped with the edge and plans nothing", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.StackState{})
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Errorf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if len(plan.GetItems()) != 0 {
			t.Errorf("items = %v, want none for a project that never deployed", plan.GetItems())
		}
	})

	t.Run("an empty stack state is stamped with the edge and plans nothing", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.StackState{})
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Errorf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if len(plan.GetItems()) != 0 {
			t.Errorf("items = %v, want none for an empty stack state", plan.GetItems())
		}
	})

	t.Run("a deployed stack plans the workers, the store and the shared substrate it leaves", func(t *testing.T) {
		t.Parallel()

		items := planFor(t, edge.StackState{Adapter: edge.Own(map[string]string{"instance": "shop-abc"})}).GetItems()
		if got := itemFor(t, items, "edge workers", "shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("edge workers action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "deployments store", "shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("deployments store action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "tag clock rows", "shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("tag clock rows action = %v, want DELETE", got)
		}
		table := itemFor(t, items, "state table", "ocel-state")
		if table.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("state table action = %v, want KEEP", table.GetAction())
		}
		if !strings.Contains(table.GetReason(), "ocel bootstrap --destroy") {
			t.Errorf("state table reason = %q, want it to name what does remove it", table.GetReason())
		}
	})
}

func TestDestroyPlanItemsPerEdge(t *testing.T) {
	t.Parallel()

	t.Run("the CloudFront edge disables its distribution before deleting it", func(t *testing.T) {
		t.Parallel()

		state := boundTo(edge.StackState{Front: "d123.cloudfront.net"}, "shop.example.com")
		items := destroyPlanItems(planEdge(t, cloudfront.Kind), projectPlanScope{
			class: bootstrap.ClassProduction, slug: "shop", record: bootstrap.StackRecord{Edge: state},
		})
		dist := itemFor(t, items, "distribution", "d123.cloudfront.net")
		if dist.GetAction() != contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE || !dist.GetSlow() {
			t.Errorf("distribution = %v (slow=%v), want DISABLE_THEN_DELETE and slow", dist.GetAction(), dist.GetSlow())
		}
		if got := itemFor(t, items, "edge routes", "shop.example.com").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("edge routes action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "deployments ledger", "production/shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("deployments ledger action = %v, want DELETE", got)
		}
	})

	t.Run("with no edge bought the APIs and their domain names go", func(t *testing.T) {
		t.Parallel()

		state := boundTo(edge.StackState{}, "shop.example.com")
		items := destroyPlanItems(planEdge(t, apigateway.Kind), projectPlanScope{
			class: bootstrap.ClassProduction, slug: "shop", record: bootstrap.StackRecord{Edge: state},
		})
		apis := itemFor(t, items, "REST APIs", "shop")
		if apis.GetAction() != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("REST APIs action = %v, want DELETE", apis.GetAction())
		}
		if !apis.GetSlow() {
			t.Error("REST APIs slow = false: API Gateway paces their deletion to one every 30 seconds, and the plan is where that is said")
		}
		if got := itemFor(t, items, "domain names", "shop.example.com").GetSlow(); got {
			t.Error("domain names slow = true, want only the quota-paced items marked slow")
		}
		if got := itemFor(t, items, "domain names", "shop.example.com").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("domain names action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "preview fallback API", "").GetAction(); got != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("preview fallback API action = %v, want KEEP", got)
		}
	})

	t.Run("a pinned certificate and a record ocel never wrote are kept with a reason", func(t *testing.T) {
		t.Parallel()

		recorded := domains.Settlement{
			Certificate: certs.Certificate{ARN: "arn:aws:acm:eu-west-1:1:certificate/ocel"},
			Validation: domains.Records{
				Written: []edge.Record{{Name: "_acme.shop.example.com", Type: edge.RecordTypeCNAME, Value: "validate.acm-validations.aws"}},
				Owed:    []edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}},
			},
			Hosts: []domains.Host{{
				Hostname:    "pinned.example.com",
				Certificate: "arn:aws:acm:eu-west-1:1:certificate/pinned",
			}},
		}
		items := destroyPlanItems(planEdge(t, cloudfront.Kind), projectPlanScope{
			class: bootstrap.ClassProduction, slug: "shop", record: productionRecord(recorded),
		})

		if got := itemFor(t, items, "certificate", "arn:aws:acm:eu-west-1:1:certificate/ocel").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("ocel-requested certificate action = %v, want DELETE", got)
		}
		pinned := itemFor(t, items, "certificate", "arn:aws:acm:eu-west-1:1:certificate/pinned")
		if pinned.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("pinned certificate action = %v, want KEEP", pinned.GetAction())
		}
		if !strings.Contains(pinned.GetReason(), "pinned for pinned.example.com") {
			t.Errorf("pinned certificate reason = %q, want it to say whose pin it is", pinned.GetReason())
		}
		owed := itemFor(t, items, "DNS record", "shop.example.com CNAME d123.cloudfront.net")
		if owed.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("owed record action = %v, want KEEP", owed.GetAction())
		}
		if !strings.Contains(owed.GetReason(), "ocel never wrote it") {
			t.Errorf("owed record reason = %q, want it to say why it stays", owed.GetReason())
		}
		if got := itemFor(t, items, "DNS record", "_acme.shop.example.com CNAME validate.acm-validations.aws").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
	})

	t.Run("an adopted certificate is never ocel's to delete", func(t *testing.T) {
		t.Parallel()

		recorded := domains.Settlement{Certificate: certs.Certificate{ARN: "arn:adopted", Adopted: true}}
		items := destroyPlanItems(planEdge(t, cloudfront.Kind), projectPlanScope{
			class: bootstrap.ClassProduction, slug: "shop", record: productionRecord(recorded),
		})
		if got := itemFor(t, items, "certificate", "arn:adopted").GetAction(); got != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("adopted certificate action = %v, want KEEP", got)
		}
	})

	t.Run("the records ocel wrote for the stack itself are planned too", func(t *testing.T) {
		t.Parallel()

		var written edge.StackState
		written.RecordWrites([]edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}})
		items := destroyPlanItems(planEdge(t, cloudfront.Kind), projectPlanScope{
			class:  bootstrap.ClassProduction,
			slug:   "shop",
			record: bootstrap.StackRecord{Edge: written},
		})
		if got := itemFor(t, items, "DNS record", "shop.example.com CNAME d123.cloudfront.net").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
	})

	t.Run("a project served on the shared preview wildcard keeps it", func(t *testing.T) {
		t.Parallel()

		items := destroyPlanItems(planEdge(t, apigateway.Kind), projectPlanScope{
			class:  bootstrap.ClassPreview,
			slug:   "shop",
			record: bootstrap.StackRecord{Edge: edge.StackState{GlobalPreview: "preview.acme.com"}},
		})
		wildcard := itemFor(t, items, "preview wildcard", "*.preview.acme.com")
		if wildcard.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("preview wildcard action = %v, want KEEP", wildcard.GetAction())
		}
		if !strings.Contains(wildcard.GetReason(), "ocel domain release --preview") {
			t.Errorf("preview wildcard reason = %q, want it to name what releases it", wildcard.GetReason())
		}
	})
}

func TestPlanRemoveProjectUnsupportedEdge(t *testing.T) {
	t.Parallel()

	client := newTestClientFor(t, &Server{}, testToken)
	_, err := client.PlanRemoveProject(context.Background(), &contractv1.PlanRemoveProjectRequest{Slug: "shop", Edge: &contractv1.EdgeSelection{Kind: "bogus"}})
	if err == nil {
		t.Fatal("PlanDestroyProject error = nil, want the unsupported edge refused")
	}
	for _, want := range []string{"bogus", string(cloudflare.Kind)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestRequestNamingNoEdgeTakesTheProviderDefault(t *testing.T) {
	t.Parallel()

	if got := requestedEdge(&contractv1.PlanRemoveProjectRequest{Slug: "shop"}); got != edges.DefaultKind {
		t.Errorf("requestedEdge() = %q, want %q: a config that names no edge lets the provider choose", got, edges.DefaultKind)
	}
	if got := requestedEdge(&contractv1.PlanRemoveProjectRequest{Slug: "shop", Edge: &contractv1.EdgeSelection{Kind: string(apigateway.Kind)}}); got != apigateway.Kind {
		t.Errorf("requestedEdge() = %q, want %q: a named edge is left alone", got, apigateway.Kind)
	}
}

func TestDestroyPlanFollowsTheEdgeTheRequestNames(t *testing.T) {
	t.Parallel()

	state := boundTo(edge.StackState{}, "shop.example.com")

	planFor := func(t *testing.T, kind edge.Kind) *contractv1.EdgeStackPlan {
		t.Helper()
		s := &Server{}
		edgeFront, err := s.edge(kind, "eu-west-1")
		if err != nil {
			t.Fatalf("edge(%q) error = %v", kind, err)
		}
		plan, err := s.edgeStackPlan(edgeFront, projectPlanScope{
			class:      bootstrap.ClassProduction,
			slug:       "shop",
			stateTable: "ocel-state",
			stacks:     1,
			record:     bootstrap.StackRecord{Edge: state},
		})
		if err != nil {
			t.Fatalf("edgeStackPlan() error = %v", err)
		}
		return plan
	}

	t.Run("a project that bought no edge plans its APIs, not Cloudflare workers", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, apigateway.Kind)
		if plan.GetEdgeKind() != string(apigateway.Kind) {
			t.Fatalf("edge_kind = %q, want apigateway: a project on the API Gateway edge must not be planned as a Cloudflare teardown", plan.GetEdgeKind())
		}
		if got := itemFor(t, plan.GetItems(), "REST APIs", "shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
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

		plan := planFor(t, cloudflare.Kind)
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Fatalf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if got := itemFor(t, plan.GetItems(), "edge workers", "shop").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("edge workers action = %v, want DELETE", got)
		}
	})
}

func TestForgetStackRecordOnlyOnceTheTeardownFinished(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	recorded := bootstrap.StackRecord{Edge: edge.StackState{Slug: "shop", Endpoint: "https://store.example"}}

	standing := func(t *testing.T, ssmc *stateSSM) bootstrap.StackRecord {
		t.Helper()
		record, err := bootstrap.ReadStackRecordFor(ctx, ssmc, bootstrap.ClassProduction, "shop")
		if err != nil {
			t.Fatalf("ReadStackRecordFor: %v", err)
		}
		return record
	}
	written := func(t *testing.T) *stateSSM {
		t.Helper()
		ssmc := &stateSSM{params: map[string]string{}}
		if err := bootstrap.WriteStackRecordFor(ctx, ssmc, bootstrap.ClassProduction, "shop", recorded); err != nil {
			t.Fatalf("WriteStackRecordFor: %v", err)
		}
		return ssmc
	}

	t.Run("a failed edge destroy leaves the state the rerun reads its progress from", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: false}
		if err := forgetStackRecord(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, errors.New("the front is on fire")); err != nil {
			t.Fatalf("forgetStackRecord: %v", err)
		}
		if got := standing(t, ssmc); got.Empty() {
			t.Fatal("the stack state was forgotten after a failed teardown: the rerun has nothing to resume from")
		}
	})

	t.Run("a torn-down edge whose later steps failed still leaves the state standing", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: true}
		if err := forgetStackRecord(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, errors.New("the asset bucket is gone")); err != nil {
			t.Fatalf("forgetStackRecord: %v", err)
		}
		if got := standing(t, ssmc); got.Empty() {
			t.Fatal("the stack state was forgotten while the purge that failed still has work left")
		}
	})

	t.Run("a clean teardown forgets it last", func(t *testing.T) {
		t.Parallel()

		ssmc := written(t)
		result := deploy.DestroyProjectResult{EdgeTornDown: true}
		if err := forgetStackRecord(ctx, ssmc, bootstrap.ClassProduction, "shop", result, recorded, nil); err != nil {
			t.Fatalf("forgetStackRecord: %v", err)
		}
		if got := standing(t, ssmc); !got.Empty() {
			t.Fatalf("stack state = %v after a finished teardown, want nothing left to resume", got)
		}
	})
}
