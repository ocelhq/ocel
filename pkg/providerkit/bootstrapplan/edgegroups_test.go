package bootstrapplan_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestEdgeGroupKeepsTheEdgesOwnKindsAndRollsTheirActionsUp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		planned []edge.PlanChange
		action  provider.ChangeAction
		reason  string
	}{
		{
			name: "nothing exists yet",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanCreate},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanCreate},
			},
			action: provider.ActionCreate,
		},
		{
			name: "everything is current",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep, Reason: "already current"},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanKeep, Reason: "already current"},
			},
			action: provider.ActionKeep,
			reason: "already current",
		},
		{
			name: "one has drifted",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanUpdate, Reason: "the deployed script differs"},
			},
			action: provider.ActionUpdate,
		},
		{
			name: "one is missing",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanCreate},
			},
			action: provider.ActionUpdate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			group, err := bootstrapplan.EdgeGroup("cloudflare", "cloudflare-edge", tc.planned)
			if err != nil {
				t.Fatalf("EdgeGroup() error = %v", err)
			}
			if group.Kind != provider.EdgeGroupKind || group.Name != "cloudflare/edge" {
				t.Errorf("group = %+v, want the cloudflare edge named under its own vendor", group)
			}
			if group.Feature != "cloudflare-edge" {
				t.Errorf("group feature = %q, want the feature the edge participates through", group.Feature)
			}
			if group.Action != tc.action || group.Reason != tc.reason {
				t.Errorf("group action = %q (%q), want %q (%q)", group.Action, group.Reason, tc.action, tc.reason)
			}
			if len(group.Changes) != len(tc.planned) {
				t.Fatalf("group has %d changes, want one per planned change", len(group.Changes))
			}
			for i, change := range group.Changes {
				if change.Kind != tc.planned[i].Kind || change.Name != tc.planned[i].Name {
					t.Errorf("change %d = %+v, want the edge's own kind and name verbatim", i, change)
				}
				if !provider.ValidChangeAction(change.Action) {
					t.Errorf("change %d is %q, which no renderer knows", i, change.Action)
				}
			}
		})
	}
}

func TestEdgeGroupRefusesAnActionNoRendererKnows(t *testing.T) {
	t.Parallel()

	_, err := bootstrapplan.EdgeGroup("cloudflare", "cloudflare-edge", []edge.PlanChange{
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

	group, err := bootstrapplan.EdgeGroup("cloudflare", "cloudflare-edge", nil)
	if err != nil {
		t.Fatalf("EdgeGroup() error = %v", err)
	}
	if group.Action == provider.ActionKeep {
		t.Errorf("group = %+v, want an edge that listed no resource not to claim it is current", group)
	}
	if group.Reason != provider.DetailUnavailable {
		t.Errorf("group reason = %q, want %q", group.Reason, provider.DetailUnavailable)
	}
}

func TestEdgeGroupFromPlanGroup(t *testing.T) {
	t.Parallel()

	t.Run("a group with no changes is a group all the same", func(t *testing.T) {
		t.Parallel()

		converted, err := bootstrapplan.EdgeGroupFromPlanGroup(edge.PlanGroup{
			Kind:   provider.EdgeGroupKind,
			Name:   "cloudflare/edge",
			Action: edge.PlanKeep,
			Reason: "bootstrap-scoped",
		})
		if err != nil {
			t.Fatalf("EdgeGroupFromPlanGroup() error = %v", err)
		}
		if converted.Action != provider.ActionKeep || converted.Reason != "bootstrap-scoped" {
			t.Errorf("group = %+v, want the kept group and the reason it is kept for", converted)
		}
		if len(converted.Changes) != 0 {
			t.Errorf("group has %+v, want the rows it was given: none", converted.Changes)
		}
	})

	t.Run("an unnamed action is refused rather than left in place", func(t *testing.T) {
		t.Parallel()

		_, err := bootstrapplan.EdgeGroupFromPlanGroup(edge.PlanGroup{})
		if err == nil {
			t.Fatal("EdgeGroupFromPlanGroup() took a group naming no action, which reads as kept while the removal deletes it")
		}
	})

	t.Run("a group whose rows name an action no renderer knows is refused", func(t *testing.T) {
		t.Parallel()

		_, err := bootstrapplan.EdgeGroupFromPlanGroup(edge.PlanGroup{
			Kind:   provider.EdgeGroupKind,
			Name:   "cloudflare/edge",
			Action: edge.PlanDelete,
			Changes: []edge.PlanChange{
				{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanAction("recreate")},
			},
		})
		if err == nil {
			t.Fatal("EdgeGroupFromPlanGroup() took a row action no renderer knows")
		}
		for _, want := range []string{"recreate", "ocel-isr-writer"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %v does not name %q", err, want)
			}
		}
	})
}
