package edgeconformance

import (
	"context"
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

	t.Run("the programmable surface is exactly the declared code needs", func(t *testing.T) {
		e, _ := suite.New(t)
		_, programmable := e.(edge.Programmable)
		wants := slices.ContainsFunc(edge.CodeNeeds(), e.Supports)
		if programmable != wants {
			t.Errorf("Programmable = %v, but Supports(edge-middleware|edge-runtime) = %v; an edge that runs code must be programmable and one that does not must not be", programmable, wants)
		}
	})

	t.Run("support is the kind's declared support", func(t *testing.T) {
		e, _ := suite.New(t)
		declared := edge.CapabilitiesOf(e.Kind()).Supported()
		supported := e.Supported()
		for _, need := range supported {
			if !edge.ValidNeed(need) {
				t.Errorf("Supported() names %q, which is not a need", need)
			}
			if !e.Supports(need) {
				t.Errorf("Supported() names %q but Supports(%q) is false", need, need)
			}
		}
		for _, need := range edge.AllNeeds() {
			if want := slices.Contains(declared, need); e.Supports(need) != want {
				t.Errorf("Supports(%q) = %v, but %s declares %v", need, e.Supports(need), e.Kind(), want)
			}
			if e.Supports(need) && !slices.Contains(supported, need) {
				t.Errorf("Supports(%q) is true but Supported() omits it", need)
			}
		}
	})

	t.Run("the flip bound is the kind's declared bound", func(t *testing.T) {
		e, _ := suite.New(t)
		bound := e.FlipBound()
		if want := edge.CapabilitiesOf(e.Kind()).FlipBound(); bound != want {
			t.Errorf("FlipBound() = %+v, want %s's declared bound %+v", bound, e.Kind(), want)
		}
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
		stack, err := e.Reconcile(ctx, spec, nil)
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

		bound := edge.BoundDomains(stack.State())
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

		if bound := edge.BoundDomains(stack.State()); len(bound) != 0 {
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

		if bound := edge.BoundDomains(stack.State()); len(bound) != 0 {
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

func reconciled(t *testing.T, suite Suite) edge.EdgeStack {
	t.Helper()
	_, stack := reconciledOn(t, suite)
	return stack
}

func reconciledOn(t *testing.T, suite Suite) (edge.Edge, edge.EdgeStack) {
	t.Helper()
	e, spec := suite.New(t)
	stack, err := e.Reconcile(context.Background(), spec, nil)
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
			stack, err := e.Reconcile(ctx, spec, edge.StackState{edge.StackKeyGlobalPreview: wildcard.BaseDomain})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !edge.ServedOnGlobalPreview(stack.State(), wildcard.BaseDomain) {
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
