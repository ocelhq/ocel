package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

type Router struct{ p *cloudflare }

var _ router.Router = Router{}

func NewRouter(namespace string) Router { return Router{p: newCloudflare(namespace)} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		FlipBound:           router.FlipBound{Typical: recordTTL},
		CachesRecords:       true,
		SignsOriginForwards: true,
		ReachesFunctions:    true,
		Dispatches:          true,
		AnswersHostnames:    true,
	}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	state := prior.Edge
	state.Slug, state.Tier = spec.Slug, spec.Tier
	return r.Open(router.StackState{Slug: spec.Slug, Tier: spec.Tier, Edge: state})
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	s := &stack{p: r.p, state: state.Edge}
	if err := state.Edge.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return routerStack{s: s}, nil
}

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	state := r.s.State()
	return router.StackState{Slug: state.Slug, Tier: state.Tier, Edge: state}
}

func (r routerStack) Ledger() router.Ledger { return r.s }

func (r routerStack) Claim(context.Context, string, string) error { return nil }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, _ progress.Progress) error {
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	return r.s.promote(ctx, flip.Promotion, flip.Pointer)
}

func (r routerStack) RemovePointer(context.Context, string, progress.Progress) error { return nil }

func (r routerStack) Destroy(context.Context) error { return nil }
