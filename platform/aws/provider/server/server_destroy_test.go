package server

import (
	"context"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestEdgeStackPlan(t *testing.T) {
	t.Parallel()

	planFor := func(t *testing.T, state edge.StackState) *deploymentsv1.EdgeStackPlan {
		t.Helper()
		s := &Server{}
		edgeFront, err := s.edge()
		if err != nil {
			t.Fatalf("edge() error = %v", err)
		}
		plan, err := s.edgeStackPlan(edgeFront, state, "shop")
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

	t.Run("a deployed stack plans the edge stack for deletion", func(t *testing.T) {
		t.Parallel()

		plan := planFor(t, edge.StackState{"instance": "shop-abc"})
		if plan.GetEdgeKind() != string(edge.KindCloudflare) {
			t.Errorf("edge_kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		items := plan.GetItems()
		if len(items) != 1 {
			t.Fatalf("items = %v, want the one edge stack", items)
		}
		item := items[0]
		if item.GetKind() != "edge stack" {
			t.Errorf("kind = %q, want the edge stack", item.GetKind())
		}
		if item.GetName() != "shop" {
			t.Errorf("name = %q, want the project slug", item.GetName())
		}
		if item.GetAction() != deploymentsv1.EdgeStackPlan_Item_ACTION_DELETE {
			t.Errorf("action = %v, want DELETE", item.GetAction())
		}
		if !strings.Contains(item.GetReason(), "deployments store") {
			t.Errorf("reason = %q, want it to say what goes with the stack", item.GetReason())
		}
	})
}

func TestPlanDestroyProjectUnsupportedEdge(t *testing.T) {
	t.Parallel()

	client := newTestClientFor(t, &Server{edgeKind: "bogus"}, testToken)
	_, err := client.PlanDestroyProject(context.Background(), &deploymentsv1.PlanDestroyProjectRequest{Slug: "shop"})
	if err == nil {
		t.Fatal("PlanDestroyProject error = nil, want the unsupported edge refused")
	}
	for _, want := range []string{"bogus", string(edge.KindCloudflare)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}
