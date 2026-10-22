package bastion

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestAPlanToRemoveATierListsTheBastionItsForwardsMadeAndNothingWhenItMadeNone(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	b := bootstrap.Engine{Clients: h.b.clients, TearDown: h.b.tearDown}

	none, err := bastionRemovals(context.Background(), b, environment.TierProduction)
	if err != nil || len(none) != 0 {
		t.Fatalf("bastionRemovals() before any forward = %v, %v, want nothing", none, err)
	}
	h.forward(t, target)
	changes, err := bastionRemovals(context.Background(), b, environment.TierProduction)

	if err != nil {
		t.Fatalf("bastionRemovals() = %v", err)
	}
	var kinds []bootstrap.Kind
	for _, change := range changes {
		kinds = append(kinds, bootstrap.Kind(change.Kind))
		if change.Action != provider.ActionDelete || change.Reason == "" {
			t.Errorf("the plan shows %s %q as %q (%q), want a delete with the reason it exists", change.Kind, change.Name, change.Action, change.Reason)
		}
	}
	if !slices.Equal(kinds, []bootstrap.Kind{bootstrap.KindService, bootstrap.KindServiceAccount}) {
		t.Errorf("the plan lists %v, want the bastion service and its account", kinds)
	}
}

func TestRemovingATierTakesItsBastionDown(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	b := bootstrap.Engine{Clients: h.b.clients, TearDown: h.b.tearDown}
	log := &fake.Log{}

	if err := removeBastion(context.Background(), b, environment.TierProduction, log); err != nil {
		t.Fatalf("removeBastion() = %v", err)
	}

	if h.run.Serving() != nil {
		t.Error("the bastion service outlived its tier")
	}
	if len(h.run.Identities().DeletedAccounts) != 1 {
		t.Errorf("deleted accounts = %v, want the bastion's", h.run.Identities().DeletedAccounts)
	}
	if want := "INFO Deleted the bastion's service account " + h.b.clients.BastionAccount(environment.TierProduction); !slices.Contains(log.Lines(), want) {
		t.Errorf("removeBastion() said %q, want %q", log.Lines(), want)
	}
}

func TestRemovingTheBastionByNamePlansItsServiceAndAccountAndTakesThemDown(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	b := bootstrap.Engine{Clients: h.b.clients, TearDown: h.b.tearDown, Records: fake.NewKeyValues(), Removable: []bootstrap.RemovablePart{Part}}
	ctx := context.Background()

	groups, err := b.PlanRemovedParts(ctx, environment.TierProduction, []string{PartName}, false, nil)
	if err != nil || len(groups) != 1 || groups[0].Part != PartName || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 2 {
		t.Fatalf("planRemovedParts() = %+v, %v, want the bastion part deleted with its service and account", groups, err)
	}
	if err := b.RemoveNamedParts(ctx, environment.TierProduction, []string{PartName}, false, nil, nil); err != nil {
		t.Fatalf("removeNamedParts() = %v", err)
	}
	if h.run.Serving() != nil {
		t.Error("the bastion service outlived its removal")
	}
}

func TestATierWithCloudRunOffDescribesPlansAndRemovesNoBastion(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	server.Accounts = map[string]bool{}
	server.ServicesOff = []string{"run.googleapis.com"}
	b := bootstrap.Engine{Clients: server.Open(t)}
	ctx := context.Background()
	tier := environment.TierProduction

	if changes, err := bastionRemovals(ctx, b, tier); err != nil || len(changes) != 0 {
		t.Errorf("bastionRemovals() with Cloud Run off = %v, %v, want nothing to remove", changes, err)
	}
	if err := removeBastion(ctx, b, tier, nil); err != nil {
		t.Errorf("removeBastion() with Cloud Run off = %v, want the tier removed: no bastion can stand where Cloud Run is off", err)
	}
	if _, present, err := describeBastionPart(ctx, b, tier); err != nil || present {
		t.Errorf("describeBastionPart() with Cloud Run off = %t, %v, want no bastion described", present, err)
	}
}
