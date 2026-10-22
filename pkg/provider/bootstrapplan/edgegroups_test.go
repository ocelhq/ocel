package bootstrapplan_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
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
				{Kind: "Cloudflare::WorkerSecret", Name: "ocel-releases-store/BOOTSTRAP_SECRET", Action: edge.PlanCreate},
				{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanCreate},
			},
			action: provider.ActionCreate,
		},
		{
			name: "everything is current",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::WorkerSecret", Name: "ocel-releases-store/BOOTSTRAP_SECRET", Action: edge.PlanKeep, Reason: "already current"},
				{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanKeep, Reason: "already current"},
			},
			action: provider.ActionKeep,
			reason: "already current",
		},
		{
			name: "one has drifted",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::WorkerSecret", Name: "ocel-releases-store/BOOTSTRAP_SECRET", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanUpdate, Reason: "the deployed script differs"},
			},
			action: provider.ActionUpdate,
		},
		{
			name: "one is missing",
			planned: []edge.PlanChange{
				{Kind: "Cloudflare::WorkerSecret", Name: "ocel-releases-store/BOOTSTRAP_SECRET", Action: edge.PlanKeep},
				{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanCreate},
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
			if group.Kind != edge.EdgeGroupKind || group.Name != "cloudflare/edge" {
				t.Errorf("group = %+v, want the cloudflare edge named under its own vendor", group)
			}
			if group.Part != "cloudflare-edge" {
				t.Errorf("group part = %q, want the part the edge participates through", group.Part)
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
		{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanAction("recreate")},
	})
	if err == nil {
		t.Fatal("EdgeGroup() took an action no renderer knows, which the CLI draws as a bare ?")
	}
	for _, want := range []string{"recreate", "ocel-releases-store"} {
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

func TestEdgeGroupRollsUpWhatTheOriginRaisesForTheEdgeAfterTheEdgesOwnChanges(t *testing.T) {
	t.Parallel()

	group, err := bootstrapplan.EdgeGroup("cloudflare", "cloudflare-edge",
		[]edge.PlanChange{{Kind: "worker", Name: "ocel-releases-store", Action: edge.PlanKeep, Reason: provider.ReasonCurrent}},
		provider.Change{Kind: "storage:hmackey", Name: "reader", Action: provider.ActionCreate, Reason: "the edge reads assets with it"},
	)
	if err != nil {
		t.Fatalf("EdgeGroup() error = %v", err)
	}
	if len(group.Changes) != 2 || group.Changes[0].Name != "ocel-releases-store" || group.Changes[1].Name != "reader" {
		t.Fatalf("group changes = %+v, want the edge's worker and then the origin's key", group.Changes)
	}
	if group.Action == provider.ActionKeep {
		t.Errorf("group action = %s, want the origin's create to roll up", group.Action)
	}
}

func TestEdgeGroupFromPlanGroup(t *testing.T) {
	t.Parallel()

	t.Run("a group with no changes is a group all the same", func(t *testing.T) {
		t.Parallel()

		converted, err := bootstrapplan.EdgeGroupFromPlanGroup(edge.PlanGroup{
			Kind:   edge.EdgeGroupKind,
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
			Kind:   edge.EdgeGroupKind,
			Name:   "cloudflare/edge",
			Action: edge.PlanDelete,
			Changes: []edge.PlanChange{
				{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanAction("recreate")},
			},
		})
		if err == nil {
			t.Fatal("EdgeGroupFromPlanGroup() took a row action no renderer knows")
		}
		for _, want := range []string{"recreate", "ocel-releases-store"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %v does not name %q", err, want)
			}
		}
	})
}

type plannedEdge struct {
	edge.Edge
	bootstrap []edge.PlanChange
	removal   []edge.PlanChange
	err       error
	hooked    bool
}

func (e plannedEdge) Kind() edge.Kind { return "cloudflare" }

func (e plannedEdge) Hooks() edge.Hooks {
	if !e.hooked {
		return edge.Hooks{}
	}
	return edge.Hooks{
		PlanBootstrap:       func(context.Context, environment.Tier) ([]edge.PlanChange, error) { return e.bootstrap, e.err },
		PlanRemoveBootstrap: func(context.Context, environment.Tier) ([]edge.PlanChange, error) { return e.removal, e.err },
	}
}

func TestAnEdgeThatPlansNothingForItsBootstrapOrItsRemovalAddsNoGroup(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]plannedEdge{
		"no plan hooks":                {},
		"plan hooks that plan nothing": {hooked: true},
	} {
		bootstrap, err := bootstrapplan.PlanEdgeBootstrap(t.Context(), front, environment.TierProduction, "cloudflare-edge")
		if err != nil || bootstrap != nil {
			t.Errorf("%s: PlanEdgeBootstrap = %+v, %v, want no group", name, bootstrap, err)
		}
		removal, err := bootstrapplan.PlanEdgeRemoval(t.Context(), front, environment.TierProduction, "cloudflare-edge")
		if err != nil || removal != nil {
			t.Errorf("%s: PlanEdgeRemoval = %+v, %v, want no group", name, removal, err)
		}
	}
}

func TestAnEdgeWhosePlanFailsIsNamedInTheError(t *testing.T) {
	t.Parallel()

	front := plannedEdge{hooked: true, err: errors.New("the account changed")}
	if _, err := bootstrapplan.PlanEdgeBootstrap(t.Context(), front, environment.TierProduction, ""); !errors.Is(err, front.err) || !strings.Contains(err.Error(), "plan the cloudflare edge bootstrap") {
		t.Errorf("PlanEdgeBootstrap = %v, want the edge's error wrapped naming the cloudflare edge bootstrap", err)
	}
	if _, err := bootstrapplan.PlanEdgeRemoval(t.Context(), front, environment.TierProduction, ""); !errors.Is(err, front.err) || !strings.Contains(err.Error(), "removing the cloudflare edge bootstrap") {
		t.Errorf("PlanEdgeRemoval = %v, want the edge's error wrapped naming the cloudflare edge bootstrap", err)
	}
}

func TestAnEdgesBootstrapPlanIsOneGroupUnderItsPartWithTheOriginsChangesAfterIt(t *testing.T) {
	t.Parallel()

	front := plannedEdge{hooked: true, bootstrap: []edge.PlanChange{{Kind: "Cloudflare::Worker", Name: "ocel-prod-releases-store", Action: edge.PlanCreate}}}
	reader := provider.Change{Kind: "storage:hmackey", Name: "reader", Action: provider.ActionCreate}

	group, err := bootstrapplan.PlanEdgeBootstrap(t.Context(), front, environment.TierProduction, "cloudflare-edge", reader)
	if err != nil {
		t.Fatalf("PlanEdgeBootstrap: %v", err)
	}
	if group == nil || group.Kind != edge.EdgeGroupKind || group.Name != "cloudflare/edge" || group.Part != "cloudflare-edge" || group.Action != provider.ActionCreate {
		t.Fatalf("group = %+v, want the cloudflare edge's group under its part, creating", group)
	}
	if len(group.Changes) != 2 || group.Changes[0].Name != "ocel-prod-releases-store" || group.Changes[1] != reader {
		t.Errorf("changes = %+v, want the edge's worker and then the origin's key", group.Changes)
	}

	onlyOrigin, err := bootstrapplan.PlanEdgeBootstrap(t.Context(), plannedEdge{}, environment.TierProduction, "cloudflare-edge", reader)
	if err != nil || onlyOrigin == nil || len(onlyOrigin.Changes) != 1 {
		t.Errorf("PlanEdgeBootstrap with only the origin's change = %+v, %v, want a group holding it", onlyOrigin, err)
	}
}

func TestAnEdgesRemovalPlanIsOneDeletingGroupUnderItsPartWithTheOriginsChangesAfterIt(t *testing.T) {
	t.Parallel()

	front := plannedEdge{hooked: true, removal: []edge.PlanChange{{Kind: "Cloudflare::OriginPullCertificate", Name: "app.com/ours", Action: edge.PlanDelete}}}
	reader := provider.Change{Kind: "storage:hmackey", Name: "reader", Action: provider.ActionDelete}

	group, err := bootstrapplan.PlanEdgeRemoval(t.Context(), front, environment.TierProduction, "cloudflare-edge", reader)
	if err != nil {
		t.Fatalf("PlanEdgeRemoval: %v", err)
	}
	if group == nil || group.Kind != edge.EdgeGroupKind || group.Name != "cloudflare/edge" || group.Part != "cloudflare-edge" || group.Action != provider.ActionDelete {
		t.Fatalf("group = %+v, want the cloudflare edge's group under its part, deleting", group)
	}
	if len(group.Changes) != 2 || group.Changes[0].Name != "app.com/ours" || group.Changes[0].Action != provider.ActionDelete || group.Changes[1] != reader {
		t.Errorf("changes = %+v, want the edge's certificate and then the origin's key", group.Changes)
	}

	refused := plannedEdge{hooked: true, removal: []edge.PlanChange{{Kind: "Cloudflare::Worker", Name: "ocel-prod-releases-store", Action: edge.PlanAction("recreate")}}}
	if _, err := bootstrapplan.PlanEdgeRemoval(t.Context(), refused, environment.TierProduction, ""); err == nil {
		t.Error("PlanEdgeRemoval took an action no renderer knows")
	}
}
