package direct_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
)

type pinRecorder struct {
	mu     sync.Mutex
	pinned []string
	refuse error
}

func (p *pinRecorder) Pin(_ context.Context, service, revision string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuse != nil {
		return p.refuse
	}
	p.pinned = append(p.pinned, service+"@"+revision)
	return nil
}

func (p *pinRecorder) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.pinned)
}

const webService = "ocel-shop-prod-web"

func fronting(t *testing.T, pins *pinRecorder) edge.EdgeStack {
	t.Helper()
	front := direct.New(fake.NewRecords(), pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return stack
}

func staged(t *testing.T, stack edge.EdgeStack, identity, revision string) {
	t.Helper()
	err := stack.Ledger().PutStaged(context.Background(), edge.DeploymentRecord{
		App:       "web",
		Identity:  identity,
		Physical:  webService,
		Revisions: map[string]string{webService: revision},
	})
	if err != nil {
		t.Fatalf("PutStaged(%s) = %v", identity, err)
	}
}

func promoted(t *testing.T, stack edge.EdgeStack, id, identity string) error {
	t.Helper()
	return stack.Promote(context.Background(), edge.Promotion{
		PromotionID: id,
		Builds:      map[string]string{"web": identity},
	}, "", edge.DiscardReporter())
}

func TestARollbackPinsCloudRunBackToTheRevisionThePromotionRecorded(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	staged(t, stack, "b1", "web-00001-abc")
	staged(t, stack, "b2", "web-00002-def")

	for _, step := range []struct{ id, identity string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		if err := promoted(t, stack, step.id, step.identity); err != nil {
			t.Fatalf("Promote(%s) = %v", step.id, err)
		}
	}

	want := []string{webService + "@web-00001-abc", webService + "@web-00002-def", webService + "@web-00001-abc"}
	if got := pins.calls(); !slices.Equal(got, want) {
		t.Errorf("the promotions pinned %v, want %v: a rollback that only writes the ledger leaves the newest revision serving every request", got, want)
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	for _, entry := range history {
		if entry.Active && entry.PromotionID != "p3" {
			t.Errorf("the ledger holds %s active, want p3", entry.PromotionID)
		}
	}
}

func TestAPromotionWhoseRecordNamesNoRevisionIsRefusedRatherThanLeftUnpinned(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	if err := stack.Ledger().PutStaged(context.Background(), edge.DeploymentRecord{
		App: "web", Identity: "b1", Physical: webService,
	}); err != nil {
		t.Fatalf("PutStaged(b1) = %v", err)
	}

	var refusal providerkit.Refusal
	err := promoted(t, stack, "p1", "b1")
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Promote(p1) = %v, want an %s refusal: a promotion that pins nothing reports a rollback it did not perform", err, providerkit.CodeInvalid)
	}
	if got := pins.calls(); len(got) != 0 {
		t.Errorf("the promotion pinned %v before refusing", got)
	}
}

func TestAPromotionThatCannotPinLeavesTheLedgerPointingWhereItDid(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	staged(t, stack, "b1", "web-00001-abc")
	if err := promoted(t, stack, "p1", "b1"); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}

	staged(t, stack, "b2", "web-00002-def")
	pins.refuse = errors.New("cloud run said no")
	if err := promoted(t, stack, "p2", "b2"); err == nil {
		t.Fatal("Promote(p2) = nil, want the pin's error: the ledger must not name a promotion that serves nothing")
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	for _, entry := range history {
		if entry.Active && entry.PromotionID != "p1" {
			t.Errorf("the ledger holds %s active after a pin that failed, want p1", entry.PromotionID)
		}
	}
}
