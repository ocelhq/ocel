package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Edge struct {
	pins pin.Pins
}

func New(pins pin.Pins) *Edge {
	return &Edge{pins: pins}
}

func (e *Edge) Kind() edge.Kind { return edge.None }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported: []edge.Need{edge.NeedStreaming},
	}
}

func (e *Edge) Hooks() edge.Hooks { return edge.Hooks{} }

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func Surface(slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), string(tier))
}

func (e *Edge) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"Cloud Run serves a project by its slug, and this stack names none")
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	return &stack{e: e, state: next}, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{e: e, state: state}, nil
}

func (e *Edge) DomainOwner(context.Context, string) (string, error) { return "", nil }

func (e *Edge) ProjectOwner(slug string, tier environment.Tier) string { return Surface(slug, tier) }

func (e *Edge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", unbindable("a preview wildcard")
}

func (e *Edge) DestroyPreviewWildcard(context.Context, string) error { return nil }

func unbindable(what string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"with no edge in front, a project is answered on the url Cloud Run gives each service and claims no hostname of its own, so %s cannot be bound to it: "+
			"name the %q edge, which provisions one load balancer per bootstrap tier at %s",
		what, alb.Kind, alb.BaselineCost)
}

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	return []edge.PlanGroup{{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "this project is answered on each service's own url, so the release surface's rows are the whole of what serves it",
	}}
}

func (e *Edge) PreviewWildcardRemovals(string) (removed, kept edge.PlanGroup) {
	return e.SharedPreviewRemoval(), edge.PlanGroup{}
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "nothing claims a preview hostname here: a preview is reached at the url Cloud Run gave the services it deployed",
	}
}

type stack struct {
	e        *Edge
	state    edge.StackState
	recorded record
}

type record struct {
	Pointers pin.Pointers `json:"pointers,omitempty"`
}

func (s *stack) keep() { s.state.Private = edge.Own(s.recorded) }

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) BindDomain(context.Context, edge.DomainBinding) error {
	return unbindable("a domain")
}

func (s *stack) UnbindDomain(_ context.Context, hostname string) error {
	s.state.Release(hostname)
	s.state.PublishAddress(hostname, "")
	return nil
}

func (s *stack) Destroy(context.Context) error { return nil }
