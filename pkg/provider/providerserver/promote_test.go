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
	stacks  map[router.Kind]router.Stack
}

func newPromoteWorld(t *testing.T) *promoteWorld {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{})
	w := &promoteWorld{
		ledger:  openProjectLedger(vendor, environment.TierProduction, promotedSlug),
		routers: vendor.Routers().(*fake.Routers),
		stacks:  map[router.Kind]router.Stack{},
	}
	for _, kind := range []router.Kind{fake.RouterRelay, fake.RouterDirect} {
		opened, err := w.routers.Open(kind)
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
		{stack: w.stacks[fake.RouterRelay], apps: []string{"web"}},
		{stack: w.stacks[fake.RouterDirect], apps: []string{"api"}},
	}
}

func (w *promoteWorld) staged(t *testing.T, id string) router.Promotion {
	t.Helper()
	builds := map[string]string{"web": "web-" + id, "api": "api-" + id}
	for app, build := range builds {
		if err := w.ledger.PutStaged(context.Background(), router.ReleaseRecord{App: app, Release: build}); err != nil {
			t.Fatalf("PutStaged(%s/%s): %v", app, build, err)
		}
	}
	return router.Promotion{PromotionID: id, Ts: 1, Releases: builds}
}

func (w *promoteWorld) promotes(t *testing.T, id string) error {
	t.Helper()
	return w.promotesReplacing(t, context.Background(), w.active(t), id)
}

func (w *promoteWorld) promotesReplacing(t *testing.T, ctx context.Context, replaces, id string) error {
	t.Helper()
	_, err := promote(ctx, w.ledger, promoteRequest{replaces: replaces, promotion: w.staged(t, id)}, w.appRouters(), progress.Discard())
	return err
}

func (w *promoteWorld) promotesServing(t *testing.T, id string, hosts, superseded, previous []edge.PreviewHost) error {
	t.Helper()
	routers := w.appRouters()
	routers[0].hosts, routers[0].superseded, routers[0].previous = hosts, superseded, previous
	_, err := promote(context.Background(), w.ledger, promoteRequest{replaces: w.active(t), promotion: w.staged(t, id), hosts: hosts, superseded: superseded, previous: previous}, routers, progress.Discard())
	return err
}

func (w *promoteWorld) servedHostnames() []string {
	return slices.Sorted(slices.Values(w.routers.DataPlane(fake.RouterRelay).ListServedHostnames()))
}

func (w *promoteWorld) serves(kind router.Kind, app string) string {
	return w.routers.DataPlane(kind).Releases(promotedSlug, environment.TierProduction, router.DefaultPointer)[app]
}

func (w *promoteWorld) active(t *testing.T) string {
	t.Helper()
	active, err := w.ledger.ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatalf("ActivePromotionID: %v", err)
	}
	return active
}

func TestAPromoteMovesEveryRouterOntoTheRecordsItsAppsStaged(t *testing.T) {
	w := newPromoteWorld(t)

	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q, want p1", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q, want web-p1", served)
	}
	if served := w.serves(fake.RouterDirect, "api"); served != "api-p1" {
		t.Errorf("the direct router serves api %q, want api-p1", served)
	}
}

func TestAPromoteARouterLeavesUnservedIsTakenBackAndTheRoutersThatMovedServeWhatItDisplaced(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	refused := errors.New("the data plane refused the write")
	w.routers.DataPlane(fake.RouterDirect).FailNextPointerMove(refused)
	err := w.promotes(t, "p2")

	var unserved router.Unserved
	if !errors.As(err, &unserved) || !errors.Is(err, refused) {
		t.Fatalf("promote(p2) with a router that refused = %v, want that refusal as router.Unserved", err)
	}
	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q after the promote was taken back, want p1, the promotion it displaced", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q after the promote was taken back, want web-p1 again", served)
	}
	if served := w.serves(fake.RouterDirect, "api"); served != "api-p1" {
		t.Errorf("the direct router serves api %q after it refused p2, want api-p1", served)
	}
}

func TestAFirstPromoteARouterLeavesUnservedLeavesNothingServedOnItsPointer(t *testing.T) {
	w := newPromoteWorld(t)

	w.routers.DataPlane(fake.RouterDirect).FailNextPointerMove(errors.New("the data plane refused the write"))
	if err := w.promotes(t, "p1"); err == nil {
		t.Fatal("promote(p1) with a router that refused = nil, want it unserved")
	}

	if active := w.active(t); active != "" {
		t.Errorf("the ledger names %q after the only promote was taken back, want nothing", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "" {
		t.Errorf("the relay router serves web %q after the only promote was taken back, want nothing", served)
	}
}

func TestTwoPromotesRacingOnOnePointerLeaveTheLedgerAndEveryRouterOnTheSameRelease(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotes(t, "p1"); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}

	var raced error
	w.routers.DataPlane(fake.RouterDirect).BeforeNextPointerMove(func() {
		raced = w.promotes(t, "p3")
	})
	err := w.promotes(t, "p2")

	if raced != nil {
		t.Fatalf("the promote that raced p2 = %v, want it to win", raced)
	}
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("promote(p2), displaced while it moved = %v, want router.Unserved", err)
	}
	if active := w.active(t); active != "p3" {
		t.Errorf("the ledger names %q, want p3, the promote that won", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p3" {
		t.Errorf("the relay router serves web %q, want web-p3, the release the ledger names", served)
	}
	if served := w.serves(fake.RouterDirect, "api"); served != "api-p3" {
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

func TestAPromoteInterruptedWhileItMovesStillTakesItsPromotionBack(t *testing.T) {
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
	direct := w.routers.DataPlane(fake.RouterDirect)
	direct.BeforeNextPointerMove(cancel)
	direct.FailNextPointerMove(errors.New("the pointer move was interrupted"))
	if err := w.promotesReplacing(t, ctx, "p1", "p2"); err == nil {
		t.Fatal("promote(p2) interrupted while it moved = nil, want it unserved")
	}

	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q after an interrupted promote, want p1: the interrupt that stopped the pointer move is not the context the take-back runs under", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p1" {
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

func TestAPromoteAnotherOvertookBeforeItsLedgerWriteIsRefusedBusyAndMovesNothing(t *testing.T) {
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
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p3" {
		t.Errorf("the relay router serves web %q, want web-p3", served)
	}
	if served := w.serves(fake.RouterDirect, "api"); served != "api-p3" {
		t.Errorf("the direct router serves api %q, want api-p3", served)
	}
}

func TestAPromoteWhoseRecordAReclaimRemovedAsItLandedIsTakenBackAndMovesNothing(t *testing.T) {
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
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q, want web-p1", served)
	}
}

func webHosts(hostnames ...string) []edge.PreviewHost {
	hosts := make([]edge.PreviewHost, 0, len(hostnames))
	for _, hostname := range hostnames {
		hosts = append(hosts, edge.PreviewHost{Hostname: hostname, App: "web"})
	}
	return hosts
}

func TestAPromotionTakenBackRoutesTheAliasesItWithdrewAndWithdrawsTheOnesItAdded(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotesServing(t, "p1", webHosts("keep", "old"), nil, nil); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}
	w.routers.DataPlane(fake.RouterDirect).FailNextPointerMove(errors.New("the direct router refused the move"))

	if err := w.promotesServing(t, "p2", webHosts("keep", "new"), webHosts("old"), webHosts("keep", "old")); err == nil {
		t.Fatal("promote(p2) with a failing direct router = nil, want it taken back")
	}

	if served, want := w.servedHostnames(), []string{"keep", "old"}; !slices.Equal(served, want) {
		t.Errorf("the relay router serves %v after p2 was taken back, want %v", served, want)
	}
	if active := w.active(t); active != "p1" {
		t.Errorf("the ledger names %q, want p1", active)
	}
	if served := w.serves(fake.RouterRelay, "web"); served != "web-p1" {
		t.Errorf("the relay router serves web %q, want web-p1", served)
	}
}

func TestAPromotionTakenBackKeepsServingAnAliasBothReleasesShare(t *testing.T) {
	w := newPromoteWorld(t)
	if err := w.promotesServing(t, "p1", webHosts("keep"), nil, nil); err != nil {
		t.Fatalf("promote(p1) = %v", err)
	}
	w.routers.DataPlane(fake.RouterDirect).FailNextPointerMove(errors.New("the direct router refused the move"))

	if err := w.promotesServing(t, "p2", webHosts("keep"), nil, webHosts("keep")); err == nil {
		t.Fatal("promote(p2) with a failing direct router = nil, want it taken back")
	}

	if served, want := w.servedHostnames(), []string{"keep"}; !slices.Equal(served, want) {
		t.Errorf("the relay router serves %v after p2 was taken back, want %v", served, want)
	}
}
