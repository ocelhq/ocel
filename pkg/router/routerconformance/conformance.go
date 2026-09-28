package routerconformance

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const App = "web"

type Fixture struct {
	Router       router.Router
	Spec         router.StackSpec
	Prior        router.StackState
	Serving      func(pointer string) string
	FailNextFlip func(err error)
}

type Suite struct {
	New      func(t *testing.T) Fixture
	Previews func(t *testing.T) Fixture
	Pointer  string
	Hostname string
	Record   func(app, build string) router.DeploymentRecord
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

		displaced := stage(t, stack, "conformance-b2", record(App, "b2"))
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

	t.Run("a flip the data plane refuses is unserved and leaves the release it served", func(t *testing.T) {
		fixture := suite.New(t)
		if fixture.FailNextFlip == nil {
			t.Fatal("the fixture cannot make the data plane refuse a flip, and every router must say when a flip did not move it")
		}
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		refused := stage(t, stack, "conformance-b2", record(App, "b2"))
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
		ctx := context.Background()
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, pointer, "conformance-b1", record(App, "b1"))

		removesPointer(t, stack, pointer)
		if served := fixture.Serving(pointer); served != "" {
			t.Errorf("%s serves %q after it was removed, want nothing", pointer, served)
		}
		if err := stack.RemovePointer(ctx, pointer, progress.DiscardProgress()); err != nil {
			t.Fatalf("RemovePointer again: %v", err)
		}
		if _, err := stack.Ledger().RemovePointer(ctx, pointer); err != nil {
			t.Fatalf("Ledger().RemovePointer again: %v", err)
		}
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

	t.Run("a reconciled stack reopens onto the same ledger", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, "", "conformance-reopen", functionRecord(App, "b1"))

		reopened, err := fixture.Router.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		requireInHistory(t, reopened, "", "conformance-reopen")
	})

	t.Run("state survives the seam it is persisted through", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		flips(t, stack, "", "conformance-persisted", functionRecord(App, "b1"))

		reopened, err := fixture.Router.Open(roundTrip(t, stack.State()))
		if err != nil {
			t.Fatalf("Open a persisted state: %v", err)
		}
		requireInHistory(t, reopened, "", "conformance-persisted")
	})

	t.Run("the ledger reports a schema version", func(t *testing.T) {
		stack := reconciled(t, suite.New(t))
		version, err := stack.Ledger().SchemaVersion(context.Background())
		if err != nil {
			t.Fatalf("SchemaVersion: %v", err)
		}
		if version <= 0 {
			t.Errorf("SchemaVersion = %d, want the schema the store speaks", version)
		}
	})

	t.Run("pruning keeps the window and reports both sides", func(t *testing.T) {
		ctx := context.Background()
		stack := reconciled(t, suite.New(t))
		ids := []string{"conformance-1", "conformance-2", "conformance-3"}
		for _, id := range ids {
			flips(t, stack, "", id, functionRecord(App, id))
		}
		result, err := stack.Ledger().Prune(ctx, 1, "")
		if err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if len(result.KeptPromotionIDs) < 1 {
			t.Errorf("KeptPromotionIDs = %v, want at least the one promotion asked for", result.KeptPromotionIDs)
		}
		if len(result.RemovedPromotionIDs) == 0 {
			t.Errorf("RemovedPromotionIDs = %v, want the %d promotions outside a window of one", result.RemovedPromotionIDs, len(ids)-1)
		}
		if inEffect := ids[len(ids)-1]; !slices.Contains(result.KeptPromotionIDs, inEffect) {
			t.Errorf("KeptPromotionIDs = %v, want the promotion in effect (%q) among them", result.KeptPromotionIDs, inEffect)
		}
		for _, id := range result.KeptPromotionIDs {
			if slices.Contains(result.RemovedPromotionIDs, id) {
				t.Errorf("promotion %q is reported both kept and removed", id)
			}
		}
		for _, id := range ids {
			if !slices.Contains(result.KeptPromotionIDs, id) && !slices.Contains(result.RemovedPromotionIDs, id) {
				t.Errorf("promotion %q is reported neither kept nor removed", id)
			}
		}
	})

	t.Run("removing a pointer from the ledger takes its promotions and leaves the rest", func(t *testing.T) {
		ctx := context.Background()
		stack := reconciled(t, suite.New(t))
		const pointed = "conformance-pointer"
		flips(t, stack, "", "unpointed", functionRecord(App, "b1"))
		flips(t, stack, pointed, "pointed", functionRecord(App, "b2"))

		if err := stack.RemovePointer(ctx, pointed, progress.DiscardProgress()); err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		result, err := stack.Ledger().RemovePointer(ctx, pointed)
		if err != nil {
			t.Fatalf("Ledger().RemovePointer: %v", err)
		}
		if !slices.Contains(result.RemovedPromotionIDs, "pointed") {
			t.Errorf("RemovedPromotionIDs = %v, want the pointer's promotion", result.RemovedPromotionIDs)
		}
		if slices.Contains(result.RemovedPromotionIDs, "unpointed") {
			t.Errorf("RemovedPromotionIDs = %v, want nothing outside the pointer", result.RemovedPromotionIDs)
		}
		for _, key := range result.RemovedRecordKeys {
			if slices.Contains(result.SurvivingRecordKeys, key) {
				t.Errorf("record key %q is reported both removed and surviving", key)
			}
		}
		if left := history(t, stack, pointed); len(left) != 0 {
			t.Errorf("history under %q = %v, want nothing after the pointer was removed", pointed, left)
		}
		requireInHistory(t, stack, "", "unpointed")
	})

	runPreviews(t, suite)
}

func runPreviews(t *testing.T, suite Suite) {
	t.Run("a preview pointer leaves nothing behind when it is removed", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this router cannot be served on a preview wildcard from the conformance suite alone")
		}
		ctx := context.Background()
		stack := reconciled(t, suite.Previews(t))
		const pointer = "conformance-preview"
		flips(t, stack, pointer, "previewed", functionRecord(App, "b1"))

		served := history(t, stack, pointer)
		if !slices.ContainsFunc(served, func(entry router.HistoryEntry) bool {
			return entry.PromotionID == "previewed" && entry.Active
		}) {
			t.Fatalf("history under %q = %v, want the promotion this preview serves marked active; a preview that never landed makes every assertion after it vacuous", pointer, served)
		}

		if err := stack.RemovePointer(ctx, pointer, progress.DiscardProgress()); err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		pruned, err := stack.Ledger().RemovePointer(ctx, pointer)
		if err != nil {
			t.Fatalf("Ledger().RemovePointer: %v", err)
		}
		if len(pruned.SurvivingPointerRecordKeys) != 0 {
			t.Errorf("RemovePointer left %v under %q, and a key per preview ever served is a retention term that grows with previews-ever", pruned.SurvivingPointerRecordKeys, pointer)
		}
		if left := history(t, stack, pointer); len(left) != 0 {
			t.Errorf("history under %q = %v, want nothing once the preview is gone", pointer, left)
		}
		removesPointer(t, stack, pointer)
	})
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

func stage(t *testing.T, stack router.Stack, promotionID string, records ...router.DeploymentRecord) router.Flip {
	t.Helper()
	flip := router.Flip{
		Promotion: router.Promotion{PromotionID: promotionID, Ts: 1, Builds: map[string]string{}},
		Records:   map[string]router.DeploymentRecord{},
	}
	for _, record := range records {
		if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
			t.Fatalf("PutStaged(%s/%s): %v", record.App, record.Build, err)
		}
		flip.Promotion.Builds[record.App] = record.Build
		flip.Records[record.App] = record
	}
	return flip
}

func flips(t *testing.T, stack router.Stack, pointer, promotionID string, records ...router.DeploymentRecord) {
	t.Helper()
	flip := stage(t, stack, promotionID, records...)
	flip.Pointer = pointer
	if err := stack.Flip(context.Background(), flip, progress.DiscardProgress()); err != nil {
		t.Fatalf("Flip(%s onto %q): %v", promotionID, pointer, err)
	}
}

func removesPointer(t *testing.T, stack router.Stack, pointer string) {
	t.Helper()
	ctx := context.Background()
	if err := stack.RemovePointer(ctx, pointer, progress.DiscardProgress()); err != nil {
		t.Fatalf("RemovePointer(%q): %v", pointer, err)
	}
	if _, err := stack.Ledger().RemovePointer(ctx, pointer); err != nil {
		t.Fatalf("Ledger().RemovePointer(%q): %v", pointer, err)
	}
}

func history(t *testing.T, stack router.Stack, pointer string) []router.HistoryEntry {
	t.Helper()
	entries, err := stack.Ledger().History(context.Background(), pointer)
	if err != nil {
		t.Fatalf("History(%q): %v", pointer, err)
	}
	return entries
}

func requireInHistory(t *testing.T, stack router.Stack, pointer, promotionID string) {
	t.Helper()
	entries := history(t, stack, pointer)
	if !slices.ContainsFunc(entries, func(entry router.HistoryEntry) bool { return entry.PromotionID == promotionID }) {
		t.Errorf("history under %q = %v, want promotion %q", pointer, entries, promotionID)
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
