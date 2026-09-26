package direct

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

const Kind edge.Kind = "direct"

type Edge struct {
	records records.Store
	pins    pin.Pins
}

func New(records records.Store, pins pin.Pins) *Edge {
	return &Edge{records: records, pins: pins}
}

func (e *Edge) Kind() edge.Kind { return Kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		Supported:       []edge.Need{edge.NeedStreaming},
		AddressesItself: true,
	}
}

func (e *Edge) Hooks() edge.Hooks { return edge.Hooks{} }

func (e *Edge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (e *Edge) Teardown(context.Context, edge.Class) error { return nil }

func Surface(slug string, class edge.Class) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), string(class))
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge serves a project by slug, and this stack names none", Kind)
	}
	next := prior
	next.Slug = spec.Slug
	next.Class = spec.Class
	s := &stack{e: e, state: next}
	if err := s.ledger().EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{e: e, state: state}, nil
}

func (e *Edge) DomainOwner(context.Context, string) (string, error) { return "", nil }

func (e *Edge) ProjectOwner(slug string, class edge.Class) string { return Surface(slug, class) }

func (e *Edge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", unbindable("a preview wildcard")
}

func (e *Edge) DestroyPreviewWildcard(context.Context, string) error { return nil }

func unbindable(what string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the %q edge answers on the url Cloud Run gives each service and claims no hostname of its own, so %s cannot be bound to it: "+
			"name the %q edge, which provisions one load balancer per bootstrap class at %s",
		Kind, what, alb.Kind, alb.BaselineCost)
}

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	return []edge.PlanGroup{{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
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
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "nothing claims a preview hostname here: a preview is reached at the url Cloud Run gave the services it deployed",
	}
}

type stack struct {
	e     *Edge
	state edge.StackState
}

var (
	_ edge.Edge      = (*Edge)(nil)
	_ edge.EdgeStack = (*stack)(nil)
)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) ledger() *kitledger.Ledger {
	return kitledger.New(s.e.records, s.state.Class, s.state.Slug)
}

func (s *stack) Ledger() edge.Ledger { return s.ledger() }

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, progress edge.Progress) error {
	return pin.Promote(ctx, s.ledger(), s.e.pins, promotion, pointer, progress)
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, _ edge.Progress) (edge.PruneResult, error) {
	return s.ledger().RemovePointer(ctx, pointer)
}

func (s *stack) BindDomain(context.Context, edge.DomainBinding) error {
	return unbindable("a domain")
}

func (s *stack) UnbindDomain(_ context.Context, hostname string) error {
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *stack) Destroy(ctx context.Context) error { return s.ledger().Destroy(ctx) }
