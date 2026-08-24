package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

const fakeStoreEndpoint = "https://store.fake"

type recordingEdge struct {
	kind        edge.Kind
	compatDate  string
	compatFlags []string

	deployed []edge.AppDeployment
	existing map[string]bool
	asked    []string

	opens        []edge.StackState
	reconciles   []edge.StackSpec
	reconcileErr error
	redeploys    int
	secret       string
	version      string

	staged          []edge.DeploymentRecord
	promotions      []edge.Promotion
	promotePointers []string
	pruned          []int
	prunePointers   []string
	removedPointers []string
	historyPointers []string
	destroyed       int
	destroyErr      error
	calls           []string
	record          func(string)

	storeSchemaVersion    *int
	storeSchemaVersionErr error

	bound       map[string]string
	history     []edge.HistoryEntry
	historyErr  error
	pruneResult edge.PruneResult

	promoted []pointedPromotion
}

type pointedPromotion struct {
	pointer   string
	promotion edge.Promotion
}

var (
	_ edge.Edge         = (*recordingEdge)(nil)
	_ edge.Programmable = (*recordingEdge)(nil)
)

func (f *recordingEdge) recordCall(call string) {
	f.calls = append(f.calls, call)
	if f.record != nil {
		f.record(call)
	}
}

func (f *recordingEdge) Kind() edge.Kind {
	if f.kind == "" {
		panic("recordingEdge: kind is required; construct it with an explicit edge.Kind")
	}
	return f.kind
}

func (f *recordingEdge) declared() edge.Edge {
	real, err := edges.EdgeFor(f.Kind(), edges.Deps{})
	if err != nil {
		panic("recordingEdge: " + err.Error())
	}
	return real
}

func (f *recordingEdge) Supported() []edge.Need { return f.declared().Supported() }

func (f *recordingEdge) FlipBound() edge.FlipBound { return f.declared().FlipBound() }

func (f *recordingEdge) Facts() edge.Facts {
	declared := f.declared().Facts()
	return edge.Facts{
		RunsCode:              true,
		ServesUnbound:         declared.ServesUnbound,
		SignsOriginForwards:   declared.SignsOriginForwards,
		InvalidatesByCacheTag: declared.InvalidatesByCacheTag,
	}
}

func (f *recordingEdge) ProjectSurfaces(scope edge.ProjectScope) []edge.Surface {
	return f.declared().ProjectSurfaces(scope)
}

func (f *recordingEdge) PreviewWildcardSurfaces(wildcard string) (edge.Surface, edge.Surface) {
	return f.declared().PreviewWildcardSurfaces(wildcard)
}

func (f *recordingEdge) SharedPreviewSurface() edge.Surface {
	return f.declared().SharedPreviewSurface()
}

func (f *recordingEdge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) Teardown(context.Context, edge.Class) error { return nil }

func (f *recordingEdge) AssembleApp(src edge.WorkerSource, r edge.Resolver) (edge.Worker, error) {
	return cloudflare.New().(edge.Programmable).AssembleApp(src, r)
}

func (f *recordingEdge) DeployApp(_ context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	f.deployed = append(f.deployed, app)
	return edge.AppResult{URL: "https://" + app.Name + ".acme.workers.dev"}, nil
}

func (f *recordingEdge) FindApp(_ context.Context, name string) (bool, error) {
	f.asked = append(f.asked, name)
	return f.existing[name], nil
}

func (f *recordingEdge) CodeRuntime() (string, []string) { return f.compatDate, f.compatFlags }

func (f *recordingEdge) DomainOwner(_ context.Context, hostname string) (string, error) {
	return f.bound[hostname], nil
}

func (f *recordingEdge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", nil
}

func (f *recordingEdge) DestroyPreviewWildcard(context.Context, string) error { return nil }

func (f *recordingEdge) Open(state edge.StackState) (edge.EdgeStack, error) {
	f.opens = append(f.opens, state)
	return &recordingStack{edge: f, state: state}, nil
}

func (f *recordingEdge) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	f.reconciles = append(f.reconciles, spec)
	if f.reconcileErr != nil {
		return nil, f.reconcileErr
	}
	if !prior.Empty() && f.version == spec.Version {
		return &recordingStack{edge: f, state: prior}, nil
	}
	f.redeploys++
	f.version = spec.Version
	if f.secret == "" {
		f.secret = "fake-secret"
	}
	return &recordingStack{edge: f, state: edge.StackState{
		Slug:     spec.Slug,
		Endpoint: fakeStoreEndpoint,
		Secret:   f.secret,
	}}, nil
}

func (f *recordingEdge) opened(t *testing.T, state edge.StackState) edge.EdgeStack {
	t.Helper()
	stack, err := f.Open(state)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return stack
}

func (f *recordingEdge) reconciled(t *testing.T, spec edge.StackSpec) edge.EdgeStack {
	t.Helper()
	stack, err := f.Reconcile(context.Background(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stack
}

type recordingStack struct {
	edge  *recordingEdge
	state edge.StackState
}

func (s *recordingStack) State() edge.StackState { return s.state }

func (s *recordingStack) Ledger() edge.Ledger { return s }

func (s *recordingStack) checkAuth() error {
	if s.edge.secret == "" || s.state.Secret != s.edge.secret {
		return fmt.Errorf("recordingEdge: unauthenticated store call; reconcile the stack first")
	}
	return nil
}

func (s *recordingStack) SchemaVersion(context.Context) (int, error) {
	if s.edge.storeSchemaVersionErr != nil {
		return 0, s.edge.storeSchemaVersionErr
	}
	if s.edge.storeSchemaVersion == nil {
		return edge.StoreSchemaVersion, nil
	}
	return *s.edge.storeSchemaVersion, nil
}

func (s *recordingStack) PutStaged(_ context.Context, record edge.DeploymentRecord) error {
	if err := s.checkAuth(); err != nil {
		return err
	}
	s.edge.staged = append(s.edge.staged, record)
	return nil
}

func (s *recordingStack) Promote(_ context.Context, promotion edge.Promotion, pointer string) error {
	if err := s.checkAuth(); err != nil {
		return err
	}
	s.edge.promotions = append(s.edge.promotions, promotion)
	s.edge.promotePointers = append(s.edge.promotePointers, pointer)
	s.edge.promoted = append([]pointedPromotion{{pointer: pointer, promotion: promotion}}, s.edge.promoted...)
	return nil
}

func (s *recordingStack) History(_ context.Context, pointer string) ([]edge.HistoryEntry, error) {
	if s.edge.historyErr != nil {
		return nil, s.edge.historyErr
	}
	if err := s.checkAuth(); err != nil {
		return nil, err
	}
	s.edge.historyPointers = append(s.edge.historyPointers, pointer)
	return s.edge.historyFor(pointer), nil
}

func (f *recordingEdge) historyFor(pointer string) []edge.HistoryEntry {
	var out []edge.HistoryEntry
	for _, p := range f.promoted {
		if p.pointer != pointer {
			continue
		}
		out = append(out, edge.HistoryEntry{Promotion: p.promotion, Active: len(out) == 0})
	}
	if pointer == "" {
		out = append(out, f.history...)
	}
	return out
}

func (s *recordingStack) Prune(_ context.Context, keepN int, pointer string) (edge.PruneResult, error) {
	if err := s.checkAuth(); err != nil {
		return edge.PruneResult{}, err
	}
	s.edge.pruned = append(s.edge.pruned, keepN)
	s.edge.prunePointers = append(s.edge.prunePointers, pointer)
	if !isZeroPruneResult(s.edge.pruneResult) {
		return s.edge.pruneResult, nil
	}
	var result edge.PruneResult
	for i, h := range s.edge.historyFor(pointer) {
		if i < keepN || h.Active {
			result.KeptPromotionIDs = append(result.KeptPromotionIDs, h.PromotionID)
		} else {
			result.RemovedPromotionIDs = append(result.RemovedPromotionIDs, h.PromotionID)
		}
	}
	return result, nil
}

func (s *recordingStack) RemovePointer(_ context.Context, pointer string) (edge.PruneResult, error) {
	if err := s.checkAuth(); err != nil {
		return edge.PruneResult{}, err
	}
	s.edge.recordCall("remove-pointer " + pointer)
	s.edge.removedPointers = append(s.edge.removedPointers, pointer)
	var removed []string
	kept := make([]pointedPromotion, 0, len(s.edge.promoted))
	for _, p := range s.edge.promoted {
		if p.pointer == pointer {
			removed = append(removed, p.promotion.PromotionID)
			continue
		}
		kept = append(kept, p)
	}
	s.edge.promoted = kept
	if !isZeroPruneResult(s.edge.pruneResult) {
		return s.edge.pruneResult, nil
	}
	return edge.PruneResult{RemovedPromotionIDs: removed}, nil
}

func (s *recordingStack) BindDomain(_ context.Context, binding edge.DomainBinding) error {
	if s.edge.bound == nil {
		s.edge.bound = map[string]string{}
	}
	s.edge.bound[binding.Hostname] = s.state.Slug
	s.state.Bind(binding.Hostname)
	switch s.edge.Kind() {
	case cloudflare.Kind:
	case apigateway.Kind:
		s.state.PublishFront(binding.Hostname, "front-"+binding.Hostname+".fake")
	default:
		s.state.Front = "front-" + s.state.Slug + ".fake"
	}
	return nil
}

func (s *recordingStack) UnbindDomain(_ context.Context, hostname string) error {
	s.edge.recordCall("unbind " + hostname)
	delete(s.edge.bound, hostname)
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *recordingStack) Destroy(ctx context.Context) error {
	if err := s.checkAuth(); err != nil {
		return err
	}
	for _, hostname := range s.state.Bound {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			return err
		}
	}
	s.edge.recordCall("destroy")
	s.edge.destroyed++
	return s.edge.destroyErr
}

func isZeroPruneResult(r edge.PruneResult) bool {
	return len(r.KeptPromotionIDs) == 0 && len(r.RemovedPromotionIDs) == 0 &&
		len(r.RemovedRecordKeys) == 0 && len(r.SurvivingRecordKeys) == 0 &&
		len(r.SurvivingPointerRecordKeys) == 0
}

func fakeEdgeOf(kind edge.Kind) edge.Edge {
	f := &recordingEdge{kind: kind}
	if slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool { return edge.Supports(f, need) }) {
		return f
	}
	return unprogrammableEdge{f}
}

func TestOriginFakeEdgeConformance(t *testing.T) {
	for _, kind := range edges.SupportedEdges() {
		t.Run(string(kind), func(t *testing.T) {
			edgeconformance.Run(t, edgeconformance.Suite{
				New: func(*testing.T) (edge.Edge, edge.StackSpec) {
					return fakeEdgeOf(kind), edge.StackSpec{
						Version: "v1",
						Class:   edge.ClassProduction,
						Slug:    "conformance",
						Program: &edge.ProgramSpec{Name: "root", StoreEndpoint: fakeStoreEndpoint},
					}
				},
				Hostname: "shop.example.com",
			})
		})
	}
}

func TestRecordingEdge(t *testing.T) {
	t.Parallel()

	t.Run("reconcile is a no-op when the version is unchanged", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()
		spec := edge.StackSpec{Version: "v1"}

		stack := f.reconciled(t, spec)
		if f.redeploys != 1 {
			t.Fatalf("redeploys = %d, want 1 after the first reconcile", f.redeploys)
		}

		again, err := f.Reconcile(ctx, spec, stack.State())
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if f.redeploys != 1 {
			t.Errorf("redeploys = %d, want 1: an unchanged version must be a no-op", f.redeploys)
		}
		if again.State().Secret != stack.State().Secret {
			t.Errorf("a no-op reconcile must hand back the same state unchanged")
		}
		if len(f.reconciles) != 2 {
			t.Errorf("expected both reconcile attempts recorded, got %d", len(f.reconciles))
		}
	})

	t.Run("reconcile redeploys on a version bump", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()

		stack := f.reconciled(t, edge.StackSpec{Version: "v1"})
		if _, err := f.Reconcile(ctx, edge.StackSpec{Version: "v2"}, stack.State()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if f.redeploys != 2 {
			t.Errorf("redeploys = %d, want 2: a version bump must not be a no-op", f.redeploys)
		}
	})

	t.Run("store ops reject an unreconciled state", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind}
		stack := f.opened(t, edge.StackState{})

		if err := stack.Ledger().PutStaged(context.Background(), edge.DeploymentRecord{App: "web", Identity: "b1"}); err == nil {
			t.Error("expected PutStaged to reject a state no reconcile ever produced")
		}
		if len(f.staged) != 0 {
			t.Errorf("expected no record staged, got %v", f.staged)
		}
	})

	t.Run("store ops record calls after reconcile", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind, history: []edge.HistoryEntry{{Promotion: edge.Promotion{PromotionID: "p1"}, Active: true}}}
		ctx := context.Background()
		stack := f.reconciled(t, edge.StackSpec{Version: "v1"})

		record := edge.DeploymentRecord{App: "web", Identity: "b1"}
		if err := stack.Ledger().PutStaged(ctx, record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
		promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1, Builds: map[string]string{"web": "b1"}}
		if err := stack.Promote(ctx, promotion, ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		history, err := stack.Ledger().History(ctx, "")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if _, err := stack.Ledger().Prune(ctx, 3, ""); err != nil {
			t.Fatalf("Prune: %v", err)
		}

		if len(f.staged) != 1 || f.staged[0].App != record.App {
			t.Errorf("staged = %v, want [%v]", f.staged, record)
		}
		if len(f.promotions) != 1 || f.promotions[0].PromotionID != promotion.PromotionID {
			t.Errorf("promotions = %v, want [%v]", f.promotions, promotion)
		}
		if !slices.ContainsFunc(history, func(h edge.HistoryEntry) bool { return h.PromotionID == "p1" }) {
			t.Errorf("History = %v, want the seeded entry", history)
		}
		if len(f.pruned) != 1 || f.pruned[0] != 3 {
			t.Errorf("pruned = %v, want [3]", f.pruned)
		}
	})

	t.Run("destroying an unreconciled stack is refused", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind, destroyErr: errors.New("boom")}
		if err := f.opened(t, edge.StackState{}).Destroy(context.Background()); err == nil {
			t.Error("expected Destroy to reject a state no reconcile ever produced")
		}
	})
}

type unprogrammableEdge struct{ edge.Edge }

func (u unprogrammableEdge) Facts() edge.Facts {
	facts := u.Edge.Facts()
	facts.RunsCode = false
	return facts
}
