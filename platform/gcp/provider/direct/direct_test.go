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
	mu        sync.Mutex
	pinned    []string
	refuse    error
	interrupt context.CancelFunc
}

func (p *pinRecorder) Pin(_ context.Context, service, revision string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.interrupt != nil {
		p.interrupt()
	}
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

type staleAt struct {
	providerkit.RecordStore
	at string
}

func (s staleAt) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if slices.Contains(record.Name, s.at) {
		return "", providerkit.ErrStale
	}
	return s.RecordStore.Write(ctx, record)
}

func TestAPromotionThatLostThePointerRacePinsNothing(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	front := direct.New(staleAt{RecordStore: fake.NewRecords(), at: "pointers"}, pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	staged(t, stack, "b1", "web-00001-abc")

	var refusal providerkit.Refusal
	if err := promoted(t, stack, "p1", "b1"); !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("Promote(p1) while the pointer moved = %v, want a %s refusal", err, providerkit.CodeBusy)
	}
	if got := pins.calls(); len(got) != 0 {
		t.Errorf("a promotion that lost the pointer pinned %v: Cloud Run then serves the loser while the ledger names the winner", got)
	}
}

type honouring struct{ providerkit.RecordStore }

func (h honouring) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	if err := ctx.Err(); err != nil {
		return providerkit.Record{}, err
	}
	return h.RecordStore.Read(ctx, name)
}

func (h honouring) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h.RecordStore.Write(ctx, record)
}

func TestAPromotionInterruptedAtItsPinStillPutsThePointerBack(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	front := direct.New(honouring{fake.NewRecords()}, pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	staged(t, stack, "b1", "web-00001-abc")
	staged(t, stack, "b2", "web-00002-def")
	if err := promoted(t, stack, "p1", "b1"); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pins.interrupt, pins.refuse = cancel, context.Canceled

	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "b2"}}, "", edge.DiscardReporter()); err == nil {
		t.Fatal("Promote(p2) interrupted at its pin = nil")
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	for _, entry := range history {
		if entry.Active && entry.PromotionID != "p1" {
			t.Errorf("the ledger holds %s active after a pin that was interrupted, want p1: the interrupt is the context the take-back ran under", entry.PromotionID)
		}
	}
}
