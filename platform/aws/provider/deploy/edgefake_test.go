package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const fakeStoreEndpoint = "https://store.fake"

type recordingEdge struct {
	kind edge.Kind

	opens        []edge.StackState
	reconciles   []edge.StackSpec
	reconcileErr error
	redeploys    int
	secret       string
	version      string

	destroyed  int
	destroyErr error
	calls      []string
	record     func(string)

	bound map[string]string
}

var _ edge.Edge = (*recordingEdge)(nil)

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

func (f *recordingEdge) Facts() edge.Facts {
	declared := f.declared().Facts()
	return edge.Facts{
		Supported:             declared.Supported,
		Compatibility:         edge.Compatibility{Date: "2025-01-01", Flags: []string{"nodejs_compat"}},
		RunsCode:              true,
		ServesUnbound:         declared.ServesUnbound,
		ProxiesRecords:        declared.ProxiesRecords,
		InvalidatesByCacheTag: declared.InvalidatesByCacheTag,
	}
}

func (f *recordingEdge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	return f.declared().ProjectRemovals(scope)
}

func (f *recordingEdge) PreviewWildcardRemovals(wildcard string) (edge.PlanGroup, edge.PlanGroup) {
	return f.declared().PreviewWildcardRemovals(wildcard)
}

func (f *recordingEdge) SharedPreviewRemoval() edge.PlanGroup {
	return f.declared().SharedPreviewRemoval()
}

func (f *recordingEdge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) Teardown(context.Context, environment.Tier) error { return nil }

func (f *recordingEdge) Hooks() edge.Hooks {
	return edge.Hooks{}
}

func (f *recordingEdge) DomainOwner(_ context.Context, hostname string) (string, error) {
	return f.bound[hostname], nil
}

func (f *recordingEdge) ProjectOwner(slug string, _ environment.Tier) string { return slug }

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

func (s *recordingStack) checkAuth() error {
	if s.edge.secret == "" || s.state.Secret != s.edge.secret {
		return fmt.Errorf("recordingEdge: unauthenticated store call; reconcile the stack first")
	}
	return nil
}

func (s *recordingStack) BindDomain(_ context.Context, binding edge.DomainBinding) error {
	if s.edge.bound == nil {
		s.edge.bound = map[string]string{}
	}
	s.edge.bound[binding.Hostname] = s.state.Slug
	s.state.Bind(binding.Hostname)
	switch s.edge.Kind() {
	case cloudflare.Kind:
		if binding.Origin != nil {
			s.state.PublishAddress(binding.Hostname, binding.Origin.Address)
		}
	case apigateway.Kind:
		s.state.PublishAddress(binding.Hostname, "front-"+binding.Hostname+".fake")
	default:
		s.state.Address = "front-" + s.state.Slug + ".fake"
	}
	return nil
}

func (s *recordingStack) UnbindDomain(_ context.Context, hostname string) error {
	s.edge.recordCall("unbind " + hostname)
	delete(s.edge.bound, hostname)
	s.state.Release(hostname)
	s.state.PublishAddress(hostname, "")
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

func fakeEdgeOf(kind edge.Kind) edge.Edge {
	f := &recordingEdge{kind: kind}
	if slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool { return edge.Supports(f, need) }) {
		return f
	}
	return codelessEdge{f}
}

func TestTheOriginsFakeEdgesBehaveAsEveryEdgeMust(t *testing.T) {
	for _, kind := range edges.SupportedEdges() {
		t.Run(string(kind), func(t *testing.T) {
			edgeconformance.Run(t, edgeconformance.Suite{
				New: func(*testing.T) (edge.Edge, edge.StackSpec) {
					return fakeEdgeOf(kind), edge.StackSpec{
						Version: "v1",
						Tier:    environment.TierProduction,
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

	t.Run("destroying an unreconciled stack is refused", func(t *testing.T) {
		t.Parallel()

		f := &recordingEdge{kind: cloudflare.Kind, destroyErr: errors.New("boom")}
		if err := f.opened(t, edge.StackState{}).Destroy(context.Background()); err == nil {
			t.Error("expected Destroy to reject a state no reconcile ever produced")
		}
	})
}

type codelessEdge struct{ edge.Edge }

func (u codelessEdge) Facts() edge.Facts {
	facts := u.Edge.Facts()
	facts.RunsCode = false
	facts.Compatibility = edge.Compatibility{}
	return facts
}
