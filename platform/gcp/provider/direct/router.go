package direct

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Router struct{ e *Edge }

var _ router.Router = Router{}

func NewRouter(e *Edge) Router { return Router{e: e} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{AddressesItself: true, ReachesFunctions: true, ReachesContainers: true}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge serves a project by slug, and this stack names none", Kind)
	}
	state := prior.Edge
	state.Slug, state.Tier = spec.Slug, spec.Tier
	return r.Open(router.StackState{Slug: spec.Slug, Tier: spec.Tier, Edge: state})
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	return routerStack{s: &stack{e: r.e, state: state.Edge}}, nil
}

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	return router.StackState{Slug: r.s.state.Slug, Tier: r.s.state.Tier, Edge: r.s.state}
}

func (r routerStack) Ledger() router.Ledger { return r.s.openLedger() }

func (r routerStack) Claim(context.Context, string, string) error { return unbindable("a domain") }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	return pin.Flip(ctx, r.s.openLedger(), r.s.e.pins, flip, progress)
}

func (r routerStack) RemovePointer(context.Context, string, progress.Progress) error { return nil }

func (r routerStack) Destroy(context.Context) error { return nil }
