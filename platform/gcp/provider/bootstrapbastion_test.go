package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func TestAPlanToRemoveATierListsTheBastionItsForwardsMadeAndNothingWhenItMadeNone(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	b := composed(bootstrap.Engine{Clients: h.b.clients, TearDown: h.b.tearDown})

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
			t.Errorf("the plan shows %s %q as %q (%q), want a delete with the reason it is not in the bootstrap's own list", change.Kind, change.Name, change.Action, change.Reason)
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
	b := composed(bootstrap.Engine{Clients: h.b.clients, TearDown: h.b.tearDown})

	if err := removeBastion(context.Background(), b, environment.TierProduction, nil); err != nil {
		t.Fatalf("removeBastion() = %v", err)
	}

	if h.run.Serving() != nil {
		t.Error("the bastion service outlived its tier")
	}
	if len(h.run.Identities().DeletedAccounts) != 1 {
		t.Errorf("deleted accounts = %v, want the bastion's", h.run.Identities().DeletedAccounts)
	}
}
