package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAPlanToRemoveATierListsTheBastionItsForwardsMadeAndNothingWhenItMadeNone(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	b := bootstrap{clients: h.b.clients, tearDown: h.b.tearDown}

	none, err := b.bastionRemovals(context.Background(), environment.TierProduction)
	if err != nil || len(none) != 0 {
		t.Fatalf("bastionRemovals() before any forward = %v, %v, want nothing", none, err)
	}
	h.forward(t, target)
	changes, err := b.bastionRemovals(context.Background(), environment.TierProduction)

	if err != nil {
		t.Fatalf("bastionRemovals() = %v", err)
	}
	var kinds []Kind
	for _, change := range changes {
		kinds = append(kinds, Kind(change.Kind))
		if change.Action != provider.ActionDelete || change.Reason == "" {
			t.Errorf("the plan shows %s %q as %q (%q), want a delete with the reason it is not in the bootstrap's own list", change.Kind, change.Name, change.Action, change.Reason)
		}
	}
	if !slices.Equal(kinds, []Kind{KindService, KindServiceAccount}) {
		t.Errorf("the plan lists %v, want the bastion service and its account", kinds)
	}
}

func TestRemovingATierTakesItsBastionDown(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	b := bootstrap{clients: h.b.clients, tearDown: h.b.tearDown}

	if err := b.removeBastion(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("removeBastion() = %v", err)
	}

	if h.run.serving() != nil {
		t.Error("the bastion service outlived its tier")
	}
	if len(h.run.identities().deletedAccounts) != 1 {
		t.Errorf("deleted accounts = %v, want the bastion's", h.run.identities().deletedAccounts)
	}
}
