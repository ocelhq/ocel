package routerconformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const App = "web"

const raceChecks = 16

type Fixture struct {
	Router       router.Router
	Spec         router.StackSpec
	Prior        router.StackState
	Serving      func(pointer string) string
	FailNextFlip func(err error)
}

type Suite struct {
	New                 func(t *testing.T) Fixture
	Previews            func(t *testing.T) Fixture
	Pointer             string
	Hostname            string
	Record              func(app, build string) router.DeploymentRecord
	TornDownWithCompute bool
}

var errDisplaced = errors.New("conformance: another promotion displaced this one while it flipped")

var errDataPlane = errors.New("conformance: the data plane refused the write")

func Run(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Hostname == "" {
		t.Fatal("the suite names no hostname for the claim checks")
	}
	pointer := suite.Pointer
	if pointer == "" {
		pointer = router.DefaultPointer
	}
	record := suite.Record
	if record == nil {
		record = functionRecord
	}

	t.Run("the flip bound is one a caller can wait out", func(t *testing.T) {
		facts := suite.New(t).Router.Facts()
		bound := facts.FlipBound
		if bound.Typical < 0 {
			t.Errorf("Facts().FlipBound.Typical = %v, want a duration a caller can wait out", bound.Typical)
		}
		if bound.Typical == 0 && bound.Published {
			t.Error("Facts().FlipBound publishes a bound it declares instant; Published is read only when Typical > 0")
		}
		if facts.CachesRecords && bound.Typical == 0 {
			t.Error("Facts().FlipBound.Typical = 0 on a router whose Facts().CachesRecords is true; a router that serves a promotion from a cached record keeps serving the old one until the cache lapses, and a caller waiting on the flip needs that bound")
		}
	})

	t.Run("a router reaches at least one kind of compute", func(t *testing.T) {
		facts := suite.New(t).Router.Facts()
		if !facts.ReachesFunctions && !facts.ReachesContainers {
			t.Error("Facts() reaches neither functions nor containers, so no release could ever be served through this router")
		}
	})

	t.Run("a flip serves the release it names on its pointer", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))
		if served := fixture.Serving(pointer); served != "b1" {
			t.Fatalf("%s serves %q after a flip onto b1, want b1", pointer, served)
		}
		flips(t, stack, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after a flip onto b2, want b2", pointer, served)
		}
	})

	t.Run("a flip whose promotion is no longer active moves nothing and is unserved", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		displaced := stage("conformance-b2", record(App, "b2"))
		displaced.Pointer = pointer
		displaced.StillActive = func(context.Context) error { return errDisplaced }
		err := stack.Flip(context.Background(), displaced, progress.DiscardProgress())
		if !errors.Is(err, errDisplaced) {
			t.Fatalf("Flip with a StillActive that refuses = %v, want that refusal", err)
		}
		var unserved router.Unserved
		if !errors.As(err, &unserved) {
			t.Errorf("Flip with a StillActive that refuses = %v, want it reported as router.Unserved: nothing moved, so the caller may unwind", err)
		}
		if served := fixture.Serving(pointer); served != "b1" {
			t.Errorf("%s serves %q after a flip StillActive refused, want the b1 it served before", pointer, served)
		}
	})

	t.Run("two promotes racing on one pointer leave it serving the release the ledger names", func(t *testing.T) {
		for check := 1; check <= raceChecks; check++ {
			reached := false
			t.Run(fmt.Sprintf("the racing promote lands after StillActive answers call %d", check), func(t *testing.T) {
				reached = racesAfterCheck(t, suite.New(t), pointer, record, check)
			})
			if !reached {
				return
			}
		}
		t.Fatalf("Flip asked StillActive more than %d times, and the suite races a promote after each one", raceChecks)
	})

	t.Run("a flip the data plane refuses is unserved and leaves the release it served", func(t *testing.T) {
		fixture := suite.New(t)
		if fixture.FailNextFlip == nil {
			t.Fatal("the fixture cannot make the data plane refuse a flip, and every router must say when a flip did not move it")
		}
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		refused := stage("conformance-b2", record(App, "b2"))
		refused.Pointer = pointer
		fixture.FailNextFlip(errDataPlane)
		err := stack.Flip(context.Background(), refused, progress.DiscardProgress())
		var unserved router.Unserved
		if !errors.As(err, &unserved) {
			t.Fatalf("Flip the data plane refused = %v, want router.Unserved", err)
		}
		if served := fixture.Serving(pointer); served != "b1" {
			t.Errorf("%s serves %q after a flip the data plane refused, want the b1 it served before", pointer, served)
		}
	})

	t.Run("removing a pointer leaves nothing served on it", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		removesPointer(t, stack, pointer)
		if served := fixture.Serving(pointer); served != "" && !suite.TornDownWithCompute {
			t.Errorf("%s serves %q after it was removed, want nothing", pointer, served)
		}
		removesPointer(t, stack, pointer)
	})

	t.Run("a router that answers hostnames takes a claim, and one that does not refuses it", func(t *testing.T) {
		ctx := context.Background()
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		if !fixture.Router.Facts().AnswersHostnames {
			err := stack.Claim(ctx, suite.Hostname, App)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Errorf("Claim on a router that answers no hostname = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			return
		}
		for range 2 {
			if err := stack.Claim(ctx, suite.Hostname, App); err != nil {
				t.Fatalf("Claim(%q): %v", suite.Hostname, err)
			}
		}
		for range 2 {
			if err := stack.Disclaim(ctx, suite.Hostname); err != nil {
				t.Fatalf("Disclaim(%q): %v", suite.Hostname, err)
			}
		}
	})

	t.Run("a reconciled stack reopens onto the data plane it flipped", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		reopened, err := fixture.Router.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		flips(t, reopened, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after the reopened stack flipped onto b2, want b2", pointer, served)
		}
	})

	t.Run("state survives the seam it is persisted through", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		reopened, err := fixture.Router.Open(roundTrip(t, stack.State()))
		if err != nil {
			t.Fatalf("Open a persisted state: %v", err)
		}
		flips(t, reopened, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after a stack reopened from its persisted state flipped onto b2, want b2", pointer, served)
		}
	})

	runPreviews(t, suite, record)
}

func runPreviews(t *testing.T, suite Suite, record func(app, build string) router.DeploymentRecord) {
	t.Run("a preview pointer leaves nothing served when it is removed", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this router cannot be served on a preview wildcard from the conformance suite alone")
		}
		fixture := suite.Previews(t)
		stack := reconciled(t, fixture)
		const pointer = "conformance-preview"
		flips(t, stack, pointer, "previewed", record(App, "b1"))
		if served := fixture.Serving(pointer); served != "b1" {
			t.Fatalf("%s serves %q, want the b1 this preview flipped onto; a preview that never landed makes every assertion after it vacuous", pointer, served)
		}

		removesPointer(t, stack, pointer)
		if served := fixture.Serving(pointer); served != "" {
			t.Errorf("%s serves %q once the preview is gone, want nothing", pointer, served)
		}
		removesPointer(t, stack, pointer)
	})
}

func racesAfterCheck(t *testing.T, fixture Fixture, pointer string, record func(app, build string) router.DeploymentRecord, check int) bool {
	t.Helper()
	ctx := context.Background()
	stack := reconciled(t, fixture)
	flips(t, stack, pointer, "conformance-b1", record(App, "b1"))
	racing, err := fixture.Router.Open(stack.State())
	if err != nil {
		t.Fatalf("Open a second stack onto the same state: %v", err)
	}

	ledger := &standInLedger{active: "conformance-b1"}
	later := stage("conformance-b3", record(App, "b3"))
	later.Pointer = pointer
	earlier := stage("conformance-b2", record(App, "b2"))
	earlier.Pointer = pointer
	var raced error
	checks := 0
	earlier.StillActive = func(ctx context.Context) error {
		checks++
		if err := ledger.stillActive(earlier.Promotion.PromotionID)(ctx); err != nil {
			return err
		}
		if checks == check {
			raced = ledger.promote(ctx, racing, later, nil)
		}
		return nil
	}

	flipped := ledger.promote(ctx, stack, earlier, earlier.StillActive)
	if checks < check {
		if check == 1 {
			t.Fatalf("Flip never asked StillActive (it returned %v), so no promote could race it", flipped)
		}
		t.Skipf("Flip asked StillActive %d times, so no promote races it after call %d", checks, check)
	}
	builds := map[string]string{"conformance-b1": "b1", "conformance-b2": "b2", "conformance-b3": "b3"}
	if served, named := fixture.Serving(pointer), builds[ledger.active]; served != named {
		t.Errorf("%s serves %q while the ledger names %s (%s): a promote whose StillActive answered before another promoted and flipped must not land over it (the flip it raced returned %v, its own flip %v)",
			pointer, served, ledger.active, named, raced, flipped)
	}
	return true
}

func functionRecord(app, build string) router.DeploymentRecord {
	entry := "conformance-prod-" + app + "-r0a1b2c3d"
	return router.DeploymentRecord{
		App:           app,
		Build:         build,
		Entry:         "/",
		EntryFunction: entry,
		FunctionURLs:  map[string]string{"/": "https://conformance-" + app + ".example.com/"},
		Revisions:     map[string]string{entry: entry + "-" + build},
	}
}

func reconciled(t *testing.T, fixture Fixture) router.Stack {
	t.Helper()
	stack, err := fixture.Router.Reconcile(context.Background(), fixture.Spec, fixture.Prior)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stack
}

func stage(promotionID string, records ...router.DeploymentRecord) router.Flip {
	flip := router.Flip{
		Promotion: router.Promotion{PromotionID: promotionID, Ts: 1, Builds: map[string]string{}},
		Records:   map[string]router.DeploymentRecord{},
	}
	for _, record := range records {
		flip.Promotion.Builds[record.App] = record.Build
		flip.Records[record.App] = record
	}
	return flip
}

func flips(t *testing.T, stack router.Stack, pointer, promotionID string, records ...router.DeploymentRecord) {
	t.Helper()
	flip := stage(promotionID, records...)
	flip.Pointer = pointer
	if err := stack.Flip(context.Background(), flip, progress.DiscardProgress()); err != nil {
		t.Fatalf("Flip(%s onto %q): %v", promotionID, pointer, err)
	}
}

func removesPointer(t *testing.T, stack router.Stack, pointer string) {
	t.Helper()
	if err := stack.RemovePointer(context.Background(), pointer, progress.DiscardProgress()); err != nil {
		t.Fatalf("RemovePointer(%q): %v", pointer, err)
	}
}

func roundTrip(t *testing.T, state router.StackState) router.StackState {
	t.Helper()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal %+v: %v", state, err)
	}
	var read router.StackState
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return read
}

type standInLedger struct {
	active string
}

func (l *standInLedger) stillActive(promotionID string) func(context.Context) error {
	return func(context.Context) error {
		if l.active != promotionID {
			return errDisplaced
		}
		return nil
	}
}

func (l *standInLedger) promote(ctx context.Context, stack router.Stack, flip router.Flip, stillActive func(context.Context) error) error {
	displaced := l.active
	l.active = flip.Promotion.PromotionID
	if stillActive == nil {
		stillActive = l.stillActive(flip.Promotion.PromotionID)
	}
	flip.StillActive = stillActive
	err := stack.Flip(ctx, flip, progress.DiscardProgress())
	var unserved router.Unserved
	if errors.As(err, &unserved) && l.active == flip.Promotion.PromotionID {
		l.active = displaced
	}
	return err
}
