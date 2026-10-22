package account

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type fakeEdge struct {
	kind        edge.Kind
	needs       []edge.Need
	out         edge.BootstrapOutput
	err         error
	onBootstrap func()
	onTeardown  func()
	bootstraps  int
	teardowns   int
	tier        environment.Tier
}

type fakeEdges struct {
	opened map[edge.Kind]edge.Edge
}

func newFakeEdges(fronts ...edge.Edge) *fakeEdges {
	registry := &fakeEdges{opened: map[edge.Kind]edge.Edge{}}
	for _, front := range fronts {
		registry.opened[front.Kind()] = front
	}
	return registry
}

func (e *fakeEdges) Open(kind edge.Kind, _ provider.Options) (edge.Edge, error) {
	if front, ok := e.opened[kind]; ok {
		return front, nil
	}
	front := &fakeEdge{kind: kind}
	e.opened[kind] = front
	return front, nil
}

func (e *fakeEdges) at(kind edge.Kind) *fakeEdge {
	front, _ := e.Open(kind, nil)
	return front.(*fakeEdge)
}

func (f *fakeEdge) Kind() edge.Kind {
	if f.kind == "" {
		return "fake"
	}
	return f.kind
}

func (f *fakeEdge) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	f.bootstraps++
	f.tier = tier
	if f.onBootstrap != nil {
		f.onBootstrap()
	}
	return f.out, f.err
}

func (f *fakeEdge) Teardown(context.Context, environment.Tier) error {
	f.teardowns++
	if f.onTeardown != nil {
		f.onTeardown()
	}
	return nil
}

func (f *fakeEdge) Facts() edge.Facts { return edge.Facts{Supported: slices.Clone(f.needs)} }

func (f *fakeEdge) Hooks() edge.Hooks { return edge.Hooks{} }

func (f *fakeEdge) Reconcile(context.Context, edge.StackSpec, edge.StackState) (edge.EdgeStack, error) {
	return nil, errors.New("bootstrap never reconciles a project stack")
}

func (f *fakeEdge) Open(edge.StackState) (edge.EdgeStack, error) {
	return nil, errors.New("bootstrap never opens a project stack")
}

func (f *fakeEdge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", errors.New("bootstrap never reconciles the preview wildcard")
}

func (f *fakeEdge) DestroyPreviewWildcard(context.Context, string) error {
	return errors.New("bootstrap never destroys the preview wildcard")
}

func (f *fakeEdge) PlanProjectRemoval(edge.ProjectScope) ([]edge.PlanGroup, error) {
	return nil, nil
}

func (f *fakeEdge) PlanPreviewWildcardRemoval(wildcard string) (edge.PlanGroup, edge.PlanGroup, error) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(f.kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind:   "Fake::PreviewEntry",
			Name:   wildcard,
			Action: edge.PlanDelete,
		}},
	}, f.PlanSharedPreviewRemoval(), nil
}

func (f *fakeEdge) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(f.kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: " + edge.PreviewEntryOwner,
	}
}

func (f *fakeEdge) DomainOwner(context.Context, string) (string, error) {
	return "", errors.New("bootstrap never reads a domain owner")
}

func (f *fakeEdge) ProjectOwner(_ context.Context, slug string, tier environment.Tier) (string, error) {
	return slug + "-" + string(tier), nil
}

var currentEdge = func() edge.Edge { return &fakeEdge{kind: "default"} }

func frontedBy(t *testing.T, ed edge.Edge) {
	t.Helper()
	previous := currentEdge
	currentEdge = func() edge.Edge { return ed }
	t.Cleanup(func() { currentEdge = previous })
}
