package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Gates interface {
	Publish(ctx context.Context, tier environment.Tier, slug string, held *gate.Gate) error
	Remove(ctx context.Context, tier environment.Tier, slug string) error
}

type Edge struct {
	namespace string
	pins      pin.Pins
	gates     Gates
}

func New(namespace string, pins pin.Pins, gates Gates) *Edge {
	return &Edge{namespace: namespace, pins: pins, gates: gates}
}

func (e *Edge) Kind() edge.Kind { return edge.None }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:     []edge.Need{edge.NeedStreaming, edge.NeedRevalidation},
		Gates:         true,
		GatesAtOrigin: true,
	}
}

func (e *Edge) Hooks() edge.Hooks {
	return edge.Hooks{PublishGate: e.publishGate}
}

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func Surface(namespace, slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), slug, naming.TierWord(tier))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"Cloud Run serves a project by its slug, and this stack names none")
	}
	next := prior
	next.Slug = spec.Slug
	next.Tier = spec.Tier
	if err := e.gates.Publish(ctx, spec.Tier, spec.Slug, spec.Gate); err != nil {
		return nil, err
	}
	return &stack{e: e, state: next}, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{e: e, state: state}, nil
}

func (e *Edge) DomainOwner(context.Context, string) (string, error) { return "", nil }

func (e *Edge) ProjectOwner(_ context.Context, slug string, tier environment.Tier) (string, error) {
	return Surface(e.namespace, slug, tier), nil
}

func (e *Edge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", unbindable("a preview wildcard")
}

func (e *Edge) DestroyPreviewWildcard(context.Context, string) error { return nil }

func unbindable(what string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s needs an edge in front of Cloud Run: name the %q edge (%s per tier)",
		what, alb.Kind, alb.BaselineCost)
}

func (e *Edge) PlanProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	return []edge.PlanGroup{{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "services answer on their own urls",
	}}, nil
}

func (e *Edge) PlanPreviewWildcardRemoval(string) (removed, kept edge.PlanGroup, err error) {
	return e.PlanSharedPreviewRemoval(), edge.PlanGroup{}, nil
}

func (e *Edge) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(edge.None),
		Action: edge.PlanKeep,
		Reason: "previews answer on their Cloud Run urls",
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
	return s.e.gates.Remove(ctx, s.state.Tier, s.state.Slug)
}
