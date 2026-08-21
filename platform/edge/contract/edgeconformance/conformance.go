package edgeconformance

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Suite struct {
	New      func(t *testing.T) (edge.Edge, edge.StackSpec)
	Hostname string
	Previews func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec)
}

func promote(t *testing.T, stack edge.EdgeStack, promotion edge.Promotion, pointer string) {
	t.Helper()

	ctx := context.Background()
	for app, identity := range promotion.Builds {
		staged := edge.DeploymentRecord{
			App:           app,
			Identity:      identity,
			Entry:         "/",
			EntryFunction: "conformance-prod-" + app + "-r0a1b2c3d",
			FunctionURLs:  map[string]string{"/": "https://conformance-" + app + ".example.com/"},
		}
		if err := stack.Ledger().PutStaged(ctx, staged); err != nil {
			t.Fatalf("PutStaged(%s/%s): %v", app, identity, err)
		}
	}
	if err := stack.Promote(ctx, promotion, pointer); err != nil {
		t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
	}
}

func Run(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Hostname == "" {
		t.Fatal("the suite names no hostname, and every edge must be able to bind one")
	}

	t.Run("the code fact and the programmable surface are one answer", func(t *testing.T) {
		e, _ := suite.New(t)
		runsCode := e.Facts().RunsCode
		if _, programmable := e.(edge.Programmable); runsCode != programmable {
			t.Errorf("Facts().RunsCode = %v, but Programmable = %v; the fact and the interface are the same claim and an edge cannot answer them differently", runsCode, programmable)
		}
		wants := slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool {
			return edge.Supports(e, need)
		})
		if runsCode != wants {
			t.Errorf("Facts().RunsCode = %v, but Supported() names a code need = %v; an edge that runs code must declare one and one that does not must declare neither", runsCode, wants)
		}
	})

	t.Run("every declared need is a need, and declared once", func(t *testing.T) {
		e, _ := suite.New(t)
		supported := e.Supported()
		for i, need := range supported {
			if !edge.ValidNeed(need) {
				t.Errorf("Supported() names %q, which is not a need", need)
			}
			if slices.Contains(supported[:i], need) {
				t.Errorf("Supported() names %q twice", need)
			}
		}
	})

	t.Run("teardown surfaces are named, reasoned and honestly actioned", func(t *testing.T) {
		e, spec := suite.New(t)

		surfaces := e.ProjectSurfaces(edge.ProjectScope{
			Slug:      spec.Slug,
			Class:     spec.Class,
			Hostnames: []string{suite.Hostname},
			Front:     "front.example.net",
		})
		if len(surfaces) == 0 {
			t.Fatal("ProjectSurfaces = none, want the surfaces a project with a bound hostname stands on")
		}
		for _, surface := range surfaces {
			checkSurface(t, "ProjectSurfaces", surface)
		}

		removed, kept := e.PreviewWildcardSurfaces("*.preview.example.com")
		checkSurface(t, "PreviewWildcardSurfaces removed", removed)
		checkSurface(t, "PreviewWildcardSurfaces kept", kept)
		if removed.Action == edge.SurfaceKeep {
			t.Error("PreviewWildcardSurfaces' removed surface is kept; releasing the wildcard must take down what holds it")
		}
		if kept.Action != edge.SurfaceKeep {
			t.Errorf("PreviewWildcardSurfaces' kept surface has action %q, want %q", kept.Action, edge.SurfaceKeep)
		}

		shared := e.SharedPreviewSurface()
		checkSurface(t, "SharedPreviewSurface", shared)
		if shared.Action != edge.SurfaceKeep {
			t.Errorf("SharedPreviewSurface action = %q, want %q: it is bootstrap-scoped", shared.Action, edge.SurfaceKeep)
		}
	})

	t.Run("an edge that runs code signs its origin forwards", func(t *testing.T) {
		e, _ := suite.New(t)
		if facts := e.Facts(); facts.RunsCode && !facts.SignsOriginForwards {
			t.Error("Facts().SignsOriginForwards = false on an edge that runs code; the code it runs at the edge reaches the origin with the credentials it was bootstrapped, so it must sign")
		}
	})

	t.Run("the flip bound is one a caller can wait out", func(t *testing.T) {
		e, _ := suite.New(t)
		bound := e.FlipBound()
		if bound.Typical < 0 {
			t.Errorf("FlipBound().Typical = %v, want a duration a caller can wait out", bound.Typical)
		}
		if bound.Typical == 0 && bound.Published {
			t.Error("FlipBound() publishes a bound it declares instant; Published is read only when Typical > 0")
		}
	})

	t.Run("a reconciled stack reopens onto the same ledger", func(t *testing.T) {
		ctx := context.Background()
		e, spec := suite.New(t)
		stack, err := e.Reconcile(ctx, spec, edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		promotion := edge.Promotion{PromotionID: "conformance-reopen", Ts: 1, Builds: map[string]string{"web": "b1"}}
		promote(t, stack, promotion, "")

		reopened, err := e.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		history, err := reopened.Ledger().History(ctx, "")
		if err != nil {
			t.Fatalf("History through the reopened stack: %v", err)
		}
		if !slices.ContainsFunc(history, func(h edge.HistoryEntry) bool { return h.PromotionID == promotion.PromotionID }) {
			t.Errorf("history through the reopened stack = %v, want the promotion the reconciled stack made", history)
		}
	})

	t.Run("the ledger reports a schema version", func(t *testing.T) {
		stack := reconciled(t, suite)
		version, err := stack.Ledger().SchemaVersion(context.Background())
		if err != nil {
			t.Fatalf("SchemaVersion: %v", err)
		}
		if version <= 0 {
			t.Errorf("SchemaVersion = %d, want the schema the store speaks", version)
		}
	})

	t.Run("a promoted record shows up in history", func(t *testing.T) {
		ctx := context.Background()
		stack := reconciled(t, suite)
		promotion := edge.Promotion{PromotionID: "conformance-1", Ts: 1, Builds: map[string]string{"web": "b1"}}
		promote(t, stack, promotion, "")
		history, err := stack.Ledger().History(ctx, "")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if !slices.ContainsFunc(history, func(h edge.HistoryEntry) bool { return h.PromotionID == promotion.PromotionID }) {
			t.Errorf("history = %v, want the promotion just made", history)
		}
	})

	t.Run("pruning keeps the window and reports both sides", func(t *testing.T) {
		ctx := context.Background()
		stack := reconciled(t, suite)
		ids := []string{"conformance-1", "conformance-2", "conformance-3"}
		for i, id := range ids {
			promote(t, stack, edge.Promotion{PromotionID: id, Ts: int64(i), Builds: map[string]string{"web": id}}, "")
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

	t.Run("removing a pointer takes its promotions and leaves the rest", func(t *testing.T) {
		ctx := context.Background()
		stack := reconciled(t, suite)
		const pointer = "conformance-pointer"
		promote(t, stack, edge.Promotion{PromotionID: "held", Ts: 1, Builds: map[string]string{"web": "b1"}}, "")
		promote(t, stack, edge.Promotion{PromotionID: "pointed", Ts: 2, Builds: map[string]string{"web": "b2"}}, pointer)

		result, err := stack.RemovePointer(ctx, pointer)
		if err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		if !slices.Contains(result.RemovedPromotionIDs, "pointed") {
			t.Errorf("RemovedPromotionIDs = %v, want the pointer's promotion", result.RemovedPromotionIDs)
		}
		if slices.Contains(result.RemovedPromotionIDs, "held") {
			t.Errorf("RemovedPromotionIDs = %v, want nothing outside the pointer", result.RemovedPromotionIDs)
		}
		for _, key := range result.RemovedRecordKeys {
			if slices.Contains(result.SurvivingRecordKeys, key) {
				t.Errorf("record key %q is reported both removed and surviving", key)
			}
		}

		left, err := stack.Ledger().History(ctx, pointer)
		if err != nil {
			t.Fatalf("History(%s): %v", pointer, err)
		}
		if len(left) != 0 {
			t.Errorf("history under %q = %v, want nothing after the pointer was removed", pointer, left)
		}
		held, err := stack.Ledger().History(ctx, "")
		if err != nil {
			t.Fatalf("History(production): %v", err)
		}
		if !slices.ContainsFunc(held, func(h edge.HistoryEntry) bool { return h.PromotionID == "held" }) {
			t.Errorf("history outside the pointer = %v, want the promotion the pointer never held", held)
		}
	})

	t.Run("binding a domain twice binds it once and shows in state", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		binding := edge.DomainBinding{Hostname: suite.Hostname}

		if err := stack.BindDomain(ctx, binding); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if err := stack.BindDomain(ctx, binding); err != nil {
			t.Fatalf("BindDomain again: %v", err)
		}

		bound := stack.State().Bound
		if len(bound) != 1 || !slices.Contains(bound, suite.Hostname) {
			t.Errorf("bound domains = %v, want exactly %q", bound, suite.Hostname)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner == "" {
			t.Errorf("DomainOwner(%q) = %q, want the surface the binding created", suite.Hostname, owner)
		}
	})

	t.Run("a bound domain has a front to point DNS at", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}

		records := frontedRecords(t, e, stack.State(), suite.Hostname)

		reopened, err := e.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		reread := frontedRecords(t, e, reopened.State(), suite.Hostname)
		if !slices.Equal(reread, records) {
			t.Errorf("records through a reopened stack = %v, want the %v the binding published", reread, records)
		}
	})

	t.Run("the binding publishes the front, not some reconcile before it", func(t *testing.T) {
		ctx := context.Background()
		e, reconciled := reconciledOn(t, suite)
		stack, err := e.Open(withoutFronts(reconciled.State()))
		if err != nil {
			t.Fatalf("Open a state carrying no front: %v", err)
		}
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}

		frontedRecords(t, e, stack.State(), suite.Hostname)
	})

	t.Run("a binding reports the state change the origin persists on", func(t *testing.T) {
		ctx := context.Background()
		_, stack := reconciledOn(t, suite)

		before := stack.State()
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if after := stack.State(); after.Equal(before) {
			t.Errorf("state = %+v both before and after %q was bound; the origin writes what a call reports as changed, so a binding that reports nothing is lost the moment the process ends", after, suite.Hostname)
		}
	})

	t.Run("state survives the seam it is persisted through", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		promotion := edge.Promotion{PromotionID: "conformance-persisted", Ts: 1, Builds: map[string]string{"web": "b1"}}
		promote(t, stack, promotion, "")

		held := stack.State()
		persisted := roundTrip(t, held)
		if !persisted.Equal(held) {
			t.Errorf("state read back = %+v, want the %+v it was written from; everything a stack keeps travels through this one encoding, including what the edge keeps to itself", persisted, held)
		}

		reopened, err := e.Open(persisted)
		if err != nil {
			t.Fatalf("Open a persisted state: %v", err)
		}
		frontedRecords(t, e, reopened.State(), suite.Hostname)
		history, err := reopened.Ledger().History(ctx, "")
		if err != nil {
			t.Fatalf("History through a persisted state: %v", err)
		}
		if !slices.ContainsFunc(history, func(h edge.HistoryEntry) bool { return h.PromotionID == promotion.PromotionID }) {
			t.Errorf("history through a persisted state = %v, want the promotion made before it was written", history)
		}
	})

	t.Run("unbinding a domain twice leaves nothing bound", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)

		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain before any binding: %v", err)
		}
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain: %v", err)
		}
		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain again: %v", err)
		}

		if bound := stack.State().Bound; len(bound) != 0 {
			t.Errorf("bound domains = %v, want none once the host is unbound", bound)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want nothing serving an unbound host", suite.Hostname, owner)
		}
	})

	runPreviews(t, suite)

	t.Run("destroying a stack takes the domains it bound with it", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}

		if err := stack.Destroy(ctx); err != nil {
			t.Fatalf("Destroy: %v", err)
		}

		if bound := stack.State().Bound; len(bound) != 0 {
			t.Errorf("bound domains = %v, want none after the stack was destroyed", bound)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want no surface left after Destroy", suite.Hostname, owner)
		}
	})
}

func checkSurface(t *testing.T, what string, surface edge.Surface) {
	t.Helper()

	if surface.Kind == "" || surface.Name == "" || surface.Reason == "" {
		t.Errorf("%s: surface %+v must carry a kind, a name and a reason", what, surface)
	}
	if !edge.ValidSurfaceAction(surface.Action) {
		t.Errorf("%s: surface %+v names no valid action", what, surface)
	}
}

func frontedRecords(t *testing.T, e edge.Edge, state edge.StackState, hostname string) []edge.Record {
	t.Helper()

	bound := state.Bound
	if !slices.Contains(bound, hostname) {
		t.Fatalf("bound domains = %v, want %q among them", bound, hostname)
	}
	target := edge.TargetFor(e, state)
	front := target.FrontFor(hostname)
	want := edge.Record{Name: hostname, Type: edge.RecordTypeCNAME, Value: front}
	if target.ServesUnbound {
		if front != "" {
			t.Errorf("the front for %q is %q, but a %s edge answers on the zone itself and publishes none", hostname, front, e.Kind())
		}
		want = edge.Record{Name: hostname, Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	} else if front == "" {
		t.Fatalf("state = %v, want a front for %q on it: a %s edge answers on a hostname of its own, and DNS has nothing to point at until the state carries it", state, hostname, e.Kind())
	}

	records, err := edge.RecordsFor(target, bound)
	if err != nil {
		t.Fatalf("RecordsFor(%v): %v", bound, err)
	}
	if len(records) != 1 || records[0] != want {
		t.Fatalf("records = %v, want [%v]", records, want)
	}
	return records
}

func roundTrip(t *testing.T, state edge.StackState) edge.StackState {
	t.Helper()

	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal %+v: %v", state, err)
	}
	var read edge.StackState
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return read
}

func withoutFronts(state edge.StackState) edge.StackState {
	state.Front, state.Fronts = "", nil
	return state
}

func reconciled(t *testing.T, suite Suite) edge.EdgeStack {
	t.Helper()
	_, stack := reconciledOn(t, suite)
	return stack
}

func reconciledOn(t *testing.T, suite Suite) (edge.Edge, edge.EdgeStack) {
	t.Helper()
	e, spec := suite.New(t)
	stack, err := e.Reconcile(context.Background(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return e, stack
}

func runPreviews(t *testing.T, suite Suite) {
	t.Run("previews served on a shared wildcard", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this edge cannot raise a preview wildcard from the conformance suite alone")
		}

		t.Run("reconciling the wildcard twice publishes the same front", func(t *testing.T) {
			ctx := context.Background()
			e, _, wildcard := suite.Previews(t)

			first, err := e.ReconcilePreviewWildcard(ctx, wildcard)
			if err != nil {
				t.Fatalf("ReconcilePreviewWildcard: %v", err)
			}
			second, err := e.ReconcilePreviewWildcard(ctx, wildcard)
			if err != nil {
				t.Fatalf("ReconcilePreviewWildcard again: %v", err)
			}
			if second != first {
				t.Errorf("front = %q on the second reconcile, want the %q the first published; a resumed `ocel domain use` must not move where DNS points", second, first)
			}
		})

		t.Run("a preview pointer on the wildcard leaves nothing behind when it is removed", func(t *testing.T) {
			ctx := context.Background()
			e, spec, wildcard := suite.Previews(t)
			if _, err := e.ReconcilePreviewWildcard(ctx, wildcard); err != nil {
				t.Fatalf("ReconcilePreviewWildcard: %v", err)
			}
			stack, err := e.Reconcile(ctx, spec, edge.StackState{GlobalPreview: wildcard.BaseDomain})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !stack.State().ServedOnGlobalPreview(wildcard.BaseDomain) {
				t.Fatalf("state = %v, want the stack to carry the wildcard it is served on", stack.State())
			}

			const pointer = "conformance-preview"
			promote(t, stack, edge.Promotion{PromotionID: "previewed", Ts: 1, Builds: map[string]string{"web": "b1"}}, pointer)

			if _, err := stack.RemovePointer(ctx, pointer); err != nil {
				t.Fatalf("RemovePointer: %v", err)
			}
			left, err := stack.Ledger().History(ctx, pointer)
			if err != nil {
				t.Fatalf("History(%s): %v", pointer, err)
			}
			if len(left) != 0 {
				t.Errorf("history under %q = %v, want nothing once the preview is gone", pointer, left)
			}
			if _, err := stack.RemovePointer(ctx, pointer); err != nil {
				t.Fatalf("RemovePointer again: %v", err)
			}
		})

		t.Run("destroying the wildcard after reconciling it is clean and re-entrant", func(t *testing.T) {
			ctx := context.Background()
			e, _, wildcard := suite.Previews(t)
			if _, err := e.ReconcilePreviewWildcard(ctx, wildcard); err != nil {
				t.Fatalf("ReconcilePreviewWildcard: %v", err)
			}
			if err := e.DestroyPreviewWildcard(ctx, wildcard.BaseDomain); err != nil {
				t.Fatalf("DestroyPreviewWildcard: %v", err)
			}
			if err := e.DestroyPreviewWildcard(ctx, wildcard.BaseDomain); err != nil {
				t.Fatalf("DestroyPreviewWildcard again: %v", err)
			}
		})
	})
}
