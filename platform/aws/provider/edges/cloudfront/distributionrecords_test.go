package cloudfront

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestAProjectsFirstDistributionIsRaisedWithoutListingTheAccount(t *testing.T) {
	t.Parallel()

	w := newWorld()
	if _, err := bootstrapped(t, w).Reconcile(context.Background(), testSpec(), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n := w.front.count("ListDistributions"); n != 0 {
		t.Errorf("ListDistributions calls = %d, want none: a page per distribution in the account grows with every other project", n)
	}
}

func TestAStackThatForgotItsDistributionFindsItByItsRecord(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	original, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	forgotten := original.State()
	forgotten.Address = ""
	forgotten.Private = edge.Own(private{})
	opened, err := e.Open(forgotten)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.front.calls = nil
	if err := opened.BindDomain(context.Background(), edge.DomainBinding{Hostname: boundHost}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if n := w.front.count("ListDistributions"); n != 0 {
		t.Errorf("ListDistributions calls = %d, want the distribution read from its record", n)
	}
	if got, want := opened.(*stack).own.Distribution, w.front.named(productionDistributionName()).id; got != want {
		t.Errorf("state records distribution %q, want %q", got, want)
	}
}

func TestAWildcardReconciledAgainListsNoDistribution(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := previewBootstrapped(t, w)
	ctx := context.Background()
	if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard again: %v", err)
	}
	if n := w.front.count("ListDistributions"); n != 0 {
		t.Errorf("ListDistributions calls = %d, want the wildcard read from its record", n)
	}
}

func TestADistributionRaisedButNeverRecordedIsAdoptedRatherThanRaisedTwice(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	w.forgetDistributionRecords()
	if _, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile after the record was lost: %v", err)
	}
	if n := len(w.front.distributions); n != 1 {
		t.Errorf("distributions = %d, want the one CloudFront already holds under the name adopted", n)
	}
}
