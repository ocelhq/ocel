package direct_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
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

func TestTheDirectRouterBehavesAsEveryRouterMust(t *testing.T) {
	routerconformance.Run(t, routerconformance.Suite{
		New: func(t *testing.T) routerconformance.Fixture {
			store, pins := fake.NewKeyValues(), &pinRecorder{}
			front := direct.New(store, pins)
			stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
			if err != nil {
				t.Fatalf("Reconcile(shop) = %v", err)
			}
			state := stack.State()
			records := ledger.New(store, state.Tier, state.Slug)
			return routerconformance.Fixture{
				Router: direct.NewRouter(front),
				Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
				Prior:  router.NewStackState(state),
				Serving: func(pointer string) string {
					history, err := records.History(context.Background(), pointer)
					if err != nil {
						t.Fatalf("History(%q) = %v", pointer, err)
					}
					calls := pins.calls()
					for _, entry := range history {
						build := entry.Builds[routerconformance.App]
						if entry.Active && len(calls) > 0 && calls[len(calls)-1] == webService+"@rev-"+build {
							return build
						}
					}
					return ""
				},
				FailNextFlip: func(err error) {
					pins.mu.Lock()
					defer pins.mu.Unlock()
					pins.refuse = err
				},
			}
		},
		Hostname: "shop.example.com",
		Record: func(app, build string) router.DeploymentRecord {
			return router.DeploymentRecord{App: app, Build: build, Physical: webService, Revisions: map[string]string{webService: "rev-" + build}}
		},
	})
}

func fronting(t *testing.T, pins *pinRecorder) router.Stack {
	t.Helper()
	return frontingOn(t, fake.NewKeyValues(), pins)
}

func frontingOn(t *testing.T, store keyvalue.Store, pins *pinRecorder) router.Stack {
	t.Helper()
	front := direct.New(store, pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	opened, err := direct.NewRouter(front).Open(router.NewStackState(stack.State()))
	if err != nil {
		t.Fatalf("Open the router = %v", err)
	}
	return opened
}

func staged(t *testing.T, stack router.Stack, identity, revision string) {
	t.Helper()
	err := stack.Ledger().PutStaged(context.Background(), router.DeploymentRecord{
		App:       "web",
		Build:     identity,
		Physical:  webService,
		Revisions: map[string]string{webService: revision},
	})
	if err != nil {
		t.Fatalf("PutStaged(%s) = %v", identity, err)
	}
}

func promoted(t *testing.T, stack router.Stack, id, identity string) error {
	t.Helper()
	return stack.Flip(context.Background(), router.Flip{Promotion: router.Promotion{
		PromotionID: id,
		Builds:      map[string]string{"web": identity},
	}}, progress.DiscardProgress())
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
			t.Errorf("the ledger records %s as active, want p3", entry.PromotionID)
		}
	}
}

func TestAPromotionSaysWhichRevisionItPinsEachAppsTrafficTo(t *testing.T) {
	t.Parallel()

	stack := fronting(t, &pinRecorder{})
	staged(t, stack, "b1", "web-00001-abc")
	progress := &fake.Progress{}

	promotion := router.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}}
	if err := stack.Flip(context.Background(), router.Flip{Promotion: promotion}, progress); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}
	want := "INFO Pinning all of web's traffic to revision web-00001-abc of Cloud Run service " + webService
	if got := progress.Lines(); !slices.Contains(got, want) {
		t.Errorf("the promotion said %q, want %q among it", got, want)
	}
}

func TestAPromotionWhoseRecordNamesNoRevisionIsRefusedRatherThanLeftUnpinned(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	if err := stack.Ledger().PutStaged(context.Background(), router.DeploymentRecord{
		App: "web", Build: "b1", Physical: webService,
	}); err != nil {
		t.Fatalf("PutStaged(b1) = %v", err)
	}

	var refused refusal.Refusal
	err := promoted(t, stack, "p1", "b1")
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Promote(p1) = %v, want an %s refusal: a promotion that pins nothing reports a rollback it did not perform", err, refusal.CodeInvalid)
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
			t.Errorf("the ledger records %s as active after a pin that failed, want p1", entry.PromotionID)
		}
	}
}

type staleAt struct {
	keyvalue.Store
	at string
}

func (s staleAt) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if slices.Contains(entry.Key.Path, s.at) {
		return "", keyvalue.ErrStale
	}
	return s.Store.Write(ctx, entry)
}

func TestAPromotionThatLostThePointerRacePinsNothing(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := frontingOn(t, staleAt{Store: fake.NewKeyValues(), at: "pointers"}, pins)
	staged(t, stack, "b1", "web-00001-abc")

	var refused refusal.Refusal
	if err := promoted(t, stack, "p1", "b1"); !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Promote(p1) while the pointer moved = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if got := pins.calls(); len(got) != 0 {
		t.Errorf("a promotion that lost the pointer pinned %v: Cloud Run then serves the loser while the ledger names the winner", got)
	}
}

type honouring struct{ keyvalue.Store }

func (h honouring) Read(ctx context.Context, name keyvalue.Key) (keyvalue.Entry, error) {
	if err := ctx.Err(); err != nil {
		return keyvalue.Entry{}, err
	}
	return h.Store.Read(ctx, name)
}

func (h honouring) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h.Store.Write(ctx, entry)
}

func TestAPromotionInterruptedAtItsPinStillPutsThePointerBack(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := frontingOn(t, honouring{fake.NewKeyValues()}, pins)
	staged(t, stack, "b1", "web-00001-abc")
	staged(t, stack, "b2", "web-00002-def")
	if err := promoted(t, stack, "p1", "b1"); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pins.interrupt, pins.refuse = cancel, context.Canceled

	if err := stack.Flip(ctx, router.Flip{Promotion: router.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "b2"}}}, progress.DiscardProgress()); err == nil {
		t.Fatal("Promote(p2) interrupted at its pin = nil")
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	for _, entry := range history {
		if entry.Active && entry.PromotionID != "p1" {
			t.Errorf("the ledger records %s as active after a pin that was interrupted, want p1: the interrupt is the context the take-back ran under", entry.PromotionID)
		}
	}
}
