package providerserver

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func openSharedStack(p provider.Provider, front edge.Edge, tier environment.Tier, slug string) (*sharedStack, error) {
	paired, err := p.Routers().Open(router.Kind(front.Kind()))
	if err != nil {
		return nil, err
	}
	return &sharedStack{front: front, router: paired, ledger: openProjectLedger(p, tier, slug)}, nil
}

type sharedStack struct {
	front   edge.Edge
	router  router.Router
	ledger  projectLedger
	mu      sync.Mutex
	current edge.EdgeStack
}

func (s *sharedStack) edgeStack() edge.EdgeStack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *sharedStack) setEdgeStack(stack edge.EdgeStack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = stack
}

func (s *sharedStack) openRouterStack() (router.Stack, error) {
	return s.router.Open(router.NewStackState(s.edgeStack().State()))
}

func (s *sharedStack) adopt(routed router.Stack) error {
	reopened, err := s.front.Open(routed.State().Edge)
	if err != nil {
		return err
	}
	s.setEdgeStack(reopened)
	return nil
}

func (s *sharedStack) promote(ctx context.Context, pointer, replaces string, promoted router.Promotion, progress progress.Progress) (router.PruneResult, error) {
	routed, err := s.openRouterStack()
	if err != nil {
		return router.PruneResult{}, err
	}
	apps := slices.Sorted(maps.Keys(promoted.Builds))
	pruned, err := promote(ctx, s.ledger, pointer, replaces, promoted, []appRouter{{stack: routed, apps: apps}}, progress)
	return pruned, errors.Join(err, s.adopt(routed))
}

func (s *sharedStack) removePointer(ctx context.Context, pointer string, progress progress.Progress) (router.PruneResult, error) {
	routed, err := s.openRouterStack()
	if err != nil {
		return router.PruneResult{}, err
	}
	if err := errors.Join(routed.RemovePointer(ctx, pointer, progress), s.adopt(routed)); err != nil {
		return router.PruneResult{}, err
	}
	return s.ledger.RemovePointer(ctx, pointer)
}

func (s *sharedStack) destroy(ctx context.Context) error {
	routed, err := s.openRouterStack()
	if err != nil {
		return err
	}
	if err := errors.Join(routed.Destroy(ctx), s.adopt(routed)); err != nil {
		return err
	}
	if err := s.edgeStack().Destroy(ctx); err != nil {
		return err
	}
	return s.ledger.Destroy(ctx)
}

func (s *sharedStack) routerState() router.StackState {
	state := s.edgeStack().State()
	return router.StackState{Slug: state.Slug, Tier: state.Tier}
}
