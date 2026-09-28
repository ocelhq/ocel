package fake

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type Routers struct{ edges *Edges }

func (e *Edges) pairings() []provider.Pairing {
	kinds := e.kinds()
	pairings := make([]provider.Pairing, 0, len(kinds))
	for _, kind := range kinds {
		pairings = append(pairings, provider.Pairing{Edge: kind, Router: router.Kind(kind), Computes: provider.Computes()})
	}
	return pairings
}

func (r Routers) Open(kind router.Kind) (router.Router, error) {
	shared := r.edges.Edge(edge.Kind(kind))
	if shared == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the reference provider routes through no %q; it routes through %s", kind, kindList(r.edges.kinds()))
	}
	return Router{edge: shared}, nil
}

type Router struct{ edge *Edge }

func (r Router) Kind() router.Kind { return router.Kind(r.edge.kind) }

func (r Router) Facts() router.Facts {
	r.edge.mu.Lock()
	defer r.edge.mu.Unlock()
	return router.Facts{
		FlipBound:             router.FlipBound{Typical: 30 * time.Second, Published: true},
		SignsOriginForwards:   true,
		RoutesPreviewsByLabel: r.edge.byLabel,
		ReachesFunctions:      true,
		ReachesContainers:     true,
		Dispatches:            r.edge.kind == KindRelay,
		AnswersHostnames:      true,
	}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	state := prior.Edge
	state.Slug, state.Tier = spec.Slug, spec.Tier
	return r.Open(router.NewStackState(state))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	shared, err := r.edge.open(state.Edge)
	if err != nil {
		return nil, err
	}
	return &RouterStack{stack: shared}, nil
}

type RouterStack struct{ stack *Stack }

func (s *RouterStack) State() router.StackState {
	state := s.stack.State()
	return router.NewStackState(state)
}

func (s *RouterStack) Ledger() router.Ledger { return s.stack.ledger }

func (s *RouterStack) Claim(context.Context, string, string) error { return nil }

func (s *RouterStack) Disclaim(context.Context, string) error { return nil }

func (s *RouterStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	if err := s.stack.front.refuseFlip(); err != nil {
		return err
	}
	if err := s.stack.ledger.Promote(ctx, flip.Promotion, flip.Pointer, progress); err != nil {
		return err
	}
	state := s.stack.State()
	s.stack.front.route(state.Slug, state.Tier, flip.Pointer, flip.Promotion.Builds)
	return nil
}

func (s *RouterStack) RemovePointer(_ context.Context, pointer string, _ progress.Progress) error {
	state := s.stack.State()
	s.stack.front.unroute(state.Slug, state.Tier, pointer)
	return nil
}

func (s *RouterStack) Destroy(context.Context) error { return nil }
