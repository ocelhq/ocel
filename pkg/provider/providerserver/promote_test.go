package providerserver

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
)

const promotedSlug = "shop"

type promoteWorld struct {
	ledger  projectLedger
	routers *fake.Routers
	stacks  map[edge.Kind]router.Stack
}

func newPromoteWorld(t *testing.T) *promoteWorld {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{})
	w := &promoteWorld{
		ledger:  openProjectLedger(vendor, environment.TierProduction, promotedSlug),
		routers: vendor.Routers().(*fake.Routers),
		stacks:  map[edge.Kind]router.Stack{},
	}
	for _, kind := range []edge.Kind{fake.KindRelay, fake.KindDirect} {
		opened, err := w.routers.Open(router.Kind(kind))
		if err != nil {
			t.Fatalf("Routers().Open(%q): %v", kind, err)
		}
		stack, err := opened.Open(router.NewStackState(edge.StackState{Slug: promotedSlug, Tier: environment.TierProduction}))
		if err != nil {
			t.Fatalf("Open(%q): %v", kind, err)
		}
		w.stacks[kind] = stack
	}
	return w
}

func (w *promoteWorld) appRouters() []appRouter {
	return []appRouter{
		{stack: w.stacks[fake.KindRelay], apps: []string{"web"}},
		{stack: w.stacks[fake.KindDirect], apps: []string{"api"}},
	}
}

func (w *promoteWorld) staged(t *testing.T, id string) router.Promotion {
	t.Helper()
	builds := map[string]string{"web": "web-" + id, "api": "api-" + id}
	for app, build := range builds {
		if err := w.ledger.PutStaged(context.Background(), router.DeploymentRecord{App: app, Build: build}); err != nil {
			t.Fatalf("PutStaged(%s/%s): %v", app, build, err)
		}
	}
	return router.Promotion{PromotionID: id, Ts: 1, Builds: builds}
}

func (w *promoteWorld) promotes(t *testing.T, id string) error {
	t.Helper()
	return w.promotesReplacing(t, context.Background(), w.active(t), id)
}

func (w *promoteWorld) promotesReplacing(t *testing.T, ctx context.Context, replaces, id string) error {
	t.Helper()
	_, err := promote(ctx, w.ledger, promoteRequest{replaces: replaces, promotion: w.staged(t, id)}, w.appRouters(), progress.DiscardProgress())
	return err
}

func (w *promoteWorld) serves(kind edge.Kind, app string) string {
	return w.routers.DataPlane(router.Kind(kind)).Builds(promotedSlug, environment.TierProduction, router.DefaultPointer)[app]
}

func (w *promoteWorld) active(t *testing.T) string {
	t.Helper()
	active, err := w.ledger.ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatalf("ActivePromotionID: %v", err)
	}
	return active
}

func TestAPromoteFlipsEveryRouterOntoTheRecordsItsAppsStaged(t *testing.T) {
	w := newPromoteWorld(t)

	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q, want p1", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q, want web-p1", served)
	}
	if served := w.serves(fake.KindDirect, "api"); served != "api-p1" {
		t.Errorf("the direct router serves api %q, want api-p1", served)
	}
}

func TestAPromoteARouterLeavesUnservedIsTakenBackAndTheRoutersThatFlippedServeWhatItDisplaced(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	refused := errors.New("the data plane refused the write")
	w.routers.DataPlane(router.Kind(fake.KindDirect)).FailNextFlip(refused)
	err := w.promotes(t, "p2")

	var unserved router.Unserved
	if !errors.As(err, &unserved) || !errors.Is(err, refused) {
		t.Fatalf("promote(p2) with a router that refused = %v, want that refusal as router.Unserved", err)
	}
	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q after the promote was taken back, want p1, the promotion it displaced", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q after the promote was taken back, want web-p1 again", served)
	}
	if served := w.serves(fake.KindDirect, "api"); served != "api-p1" {
		t.Errorf("the direct router serves api %q after it refused p2, want api-p1", served)
	}
}

func TestAFirstPromoteARouterLeavesUnservedLeavesNothingServedOnItsPointer(t *testing.T) {
	w := newPromoteWorld(t)

	w.routers.DataPlane(router.Kind(fake.KindDirect)).FailNextFlip(errors.New("the data plane refused the write"))
	if err := w.promotes(t, "p1"); err == nil {
		t.Fatal("promote(p1) with a router that refused = nil, want it unserved")
	}

	if active := w.active(t); active != "" {
		t.Errorf("the ledger names %q after the only promote was taken back, want nothing", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "" {
		t.Errorf("the relay router serves web %q after the only promote was taken back, want nothing", served)
	}
}

func TestTwoPromotesRacingOnOnePointerLeaveTheLedgerAndEveryRouterOnTheSameRelease(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	var raced error
	w.routers.DataPlane(router.Kind(fake.KindDirect)).BeforeNextFlip(func() {
		raced = w.promotes(t, "p3")
	})
	err := w.promotes(t, "p2")

	if raced != nil {
		t.Fatalf("the promote that raced p2 = %v, want it to win", raced)
	}
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("promote(p2), displaced while it flipped = %v, want router.Unserved", err)
	}
	if active := w.active(t); active != "p3" {
		t.Errorf("the ledger names %q, want p3, the promote that won", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p3" {
		t.Errorf("the relay router serves web %q, want web-p3, the release the ledger names", served)
	}
	if served := w.serves(fake.KindDirect, "api"); served != "api-p3" {
		t.Errorf("the direct router serves api %q, want api-p3, the release the ledger names", served)
	}
}

type honouring struct{ keyvalue.Store }

func (h honouring) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := ctx.Err(); err != nil {
		return keyvalue.Entry{}, err
	}
	return h.Store.Read(ctx, key)
}

func (h honouring) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h.Store.Write(ctx, entry)
}

func TestAPromoteInterruptedWhileItFlipsStillTakesItsPromotionBack(t *testing.T) {
	w := newPromoteWorld(t)
	vendor := fake.NewProvider(fake.Options{})
	w.ledger = projectLedger{
		Ledger: ledger.New(honouring{vendor.KeyValues()}, environment.TierProduction, promotedSlug),
		cipher: vendor.Cipher(),
		tier:   environment.TierProduction,
		slug:   promotedSlug,
	}
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	direct := w.routers.DataPlane(router.Kind(fake.KindDirect))
	direct.BeforeNextFlip(cancel)
	direct.FailNextFlip(errors.New("the flip was interrupted"))
	if err := w.promotesReplacing(t, ctx, "p1", "p2"); err == nil {
		t.Fatal("promote(p2) interrupted while it flipped = nil, want it unserved")
	}

	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q after an interrupted promote, want p1: the interrupt that stopped the flip is not the context the take-back runs under", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q after an interrupted promote, want web-p1 again", served)
	}
}

type overtaking struct {
	keyvalue.Store
	mu     sync.Mutex
	before func()
}

func (o *overtaking) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	o.mu.Lock()
	before := o.before
	if slices.Equal(entry.Key.Path, []string{"pointers", router.DefaultPointer}) {
		o.before = nil
	} else {
		before = nil
	}
	o.mu.Unlock()
	if before != nil {
		before()
	}
	return o.Store.Write(ctx, entry)
}

func TestAPromoteAnotherOvertookBeforeItsLedgerWriteIsRefusedBusyAndFlipsNothing(t *testing.T) {
	w := newPromoteWorld(t)
	vendor := fake.NewProvider(fake.Options{})
	store := &overtaking{Store: vendor.KeyValues()}
	w.ledger = projectLedger{
		Ledger: ledger.New(store, environment.TierProduction, promotedSlug),
		cipher: vendor.Cipher(),
		tier:   environment.TierProduction,
		slug:   promotedSlug,
	}
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	var raced error
	store.mu.Lock()
	store.before = func() { raced = w.promotes(t, "p3") }
	store.mu.Unlock()
	err := w.promotesReplacing(t, context.Background(), "p1", "p2")

	if raced != nil {
		t.Fatalf("the promote that overtook p2 = %v, want it served", raced)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("promote(p2) over p1 once p3 had landed = %v, want a busy refusal", err)
	}
	if active := w.active(t); active != "p3" {
		t.Errorf("the ledger names %q, want p3, the promote that won", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p3" {
		t.Errorf("the relay router serves web %q, want web-p3", served)
	}
	if served := w.serves(fake.KindDirect, "api"); served != "api-p3" {
		t.Errorf("the direct router serves api %q, want api-p3", served)
	}
}

func TestAPromoteWhoseRecordAReclaimRemovedAsItLandedIsTakenBackAndFlipsNothing(t *testing.T) {
	w := newPromoteWorld(t)
	vendor := fake.NewProvider(fake.Options{})
	store := &overtaking{Store: vendor.KeyValues()}
	w.ledger = projectLedger{
		Ledger: ledger.New(store, environment.TierProduction, promotedSlug),
		cipher: vendor.Cipher(),
		tier:   environment.TierProduction,
		slug:   promotedSlug,
	}
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	record := ledger.Partition(environment.TierProduction, promotedSlug).Key("records", "web", "web-p2")
	store.mu.Lock()
	store.before = func() {
		if err := keyvalue.Forget(context.Background(), vendor.KeyValues(), record); err != nil {
			t.Errorf("forget web-p2 = %v", err)
		}
	}
	store.mu.Unlock()
	err := w.promotesReplacing(t, context.Background(), "p1", "p2")

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("promote(p2) whose record a reclaim removed = %v, want a busy refusal", err)
	}
	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q, want p1: p2 was taken back", active)
	}
	if served := w.serves(fake.KindRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q, want web-p1", served)
	}
}
