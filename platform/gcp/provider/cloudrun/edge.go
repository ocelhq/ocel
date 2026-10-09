package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Gates interface {
	Publish(ctx context.Context, slug string, gate *previewgate.Gate) error
	Remove(ctx context.Context, slug string) error
}

type Edge struct {
	pins  pin.Pins
	gates Gates
}

func New(pins pin.Pins, gates Gates) *Edge {
	return &Edge{pins: pins, gates: gates}
}

func (e *Edge) Kind() edge.Kind { return edge.None }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:             []edge.Need{edge.NeedStreaming, edge.NeedRevalidation},
		GatesPreviews:         true,
		GatesPreviewsAtOrigin: true,
	}
}

func (e *Edge) Hooks() edge.Hooks {
	return edge.Hooks{PublishPreviewGate: e.publishPreviewGate}
}

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func Surface(slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), string(tier))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"Cloud Run serves a project by its slug, and this stack names none")
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	if spec.Tier == environment.TierPreview {
		if err := e.gates.Publish(ctx, spec.Slug, spec.PreviewGate); err != nil {
			return nil, err
		}
	}
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

func (*Edge) PreviewWildcardBehind(context.Context, edge.PreviewWildcardSpec) (bool, error) {
	return false, nil
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
	Pointers    pin.Pointers        `json:"pointers,omitempty"`
	Deployments map[string]pin.Tags `json:"deployments,omitempty"`
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

func (s *stack) Destroy(ctx context.Context) error {
	if s.state.Tier != environment.TierPreview {
		return nil
	}
	return s.e.gates.Remove(ctx, s.state.Slug)
}
