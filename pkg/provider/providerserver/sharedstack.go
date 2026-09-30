package providerserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func findEdgeRouter(p provider.Provider, front edge.Kind) (router.Kind, error) {
	kinds := p.Facts().ListPairedRouters(front)
	if len(kinds) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this provider serves no project through %s", describeFront(front))
	}
	return kinds[0], nil
}

func readEdgeRouter(p provider.Provider, front edge.Kind) router.Kind {
	kind, _ := findEdgeRouter(p, front)
	return kind
}

func openEdgeRouter(p provider.Provider, front edge.Kind) (router.Router, error) {
	kind, err := findEdgeRouter(p, front)
	if err != nil {
		return nil, err
	}
	return p.Routers().Open(kind)
}

func routerOriginBehind(front edge.Edge, paired router.Router) *router.OriginHooks {
	if !front.Facts().ProxiesRecords {
		return nil
	}
	return paired.Hooks().Origin
}

func openSharedStack(p provider.Provider, front edge.Edge, tier environment.Tier, slug string) (*sharedStack, error) {
	opened := map[router.Kind]router.Router{}
	for _, kind := range p.Facts().ListPairedRouters(front.Kind()) {
		paired, err := p.Routers().Open(kind)
		if err != nil {
			return nil, err
		}
		opened[kind] = paired
	}
	edgeRouter, err := openEdgeRouter(p, front.Kind())
	if err != nil {
		return nil, err
	}
	return &sharedStack{
		front:   front,
		router:  edgeRouter,
		routers: opened,
		states:  map[router.Kind]router.StackState{},
		ledger:  openProjectLedger(p, tier, slug),
	}, nil
}

type sharedStack struct {
	front   edge.Edge
	router  router.Router
	routers map[router.Kind]router.Router
	ledger  projectLedger
	mu      sync.Mutex
	current edge.EdgeStack
	states  map[router.Kind]router.StackState
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

func (s *sharedStack) routerOf(kind router.Kind) router.Router {
	if paired, found := s.routers[kind]; found {
		return paired
	}
	return s.router
}

func (s *sharedStack) routerOrigin(kind router.Kind) *router.OriginHooks {
	return routerOriginBehind(s.front, s.routerOf(kind))
}

func (s *sharedStack) keepsEdgeState(kind router.Kind) bool {
	return s.routerOf(kind).Kind() == s.router.Kind()
}

func (s *sharedStack) restoreRouterStates(recorded stackrecords.EdgeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for kind, state := range recorded.Routers {
		if kind != s.router.Kind() {
			s.states[kind] = state
		}
	}
}

func (s *sharedStack) recordRouterStates(into *stackrecords.EdgeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for kind, state := range s.states {
		if into.Routers == nil {
			into.Routers = map[router.Kind]router.StackState{}
		}
		into.Routers[kind] = state
	}
}

func (s *sharedStack) listRouterKinds() []router.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := []router.Kind{s.router.Kind()}
	for _, kind := range slices.Sorted(maps.Keys(s.states)) {
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (s *sharedStack) openRouterStack(kind router.Kind) (router.Stack, error) {
	shared := s.edgeStack().State()
	if s.keepsEdgeState(kind) {
		return s.router.Open(router.NewStackState(shared))
	}
	s.mu.Lock()
	own := s.states[kind]
	s.mu.Unlock()
	return s.routerOf(kind).Open(own.WithSpec(router.StackSpec{Tier: shared.Tier, Slug: shared.Slug}))
}

func (s *sharedStack) adopt(kind router.Kind, routed router.Stack) error {
	if !s.keepsEdgeState(kind) {
		s.mu.Lock()
		s.states[kind] = routed.State()
		s.mu.Unlock()
		return nil
	}
	reopened, err := s.front.Open(routed.State().Edge)
	if err != nil {
		return err
	}
	s.setEdgeStack(reopened)
	return nil
}

func (s *sharedStack) propagation(kinds []router.Kind) router.Propagation {
	slowest := s.router.Facts().Propagation
	for _, kind := range kinds {
		if propagation := s.routerOf(kind).Facts().Propagation; propagation.Typical > slowest.Typical {
			slowest = propagation
		}
	}
	return slowest
}

func (s *sharedStack) promoteApps(ctx context.Context, req promoteRequest, routedBy func(app string) router.Kind, progress progress.Log) ([]ledger.RecordedPromotion, error) {
	byRouter := map[router.Kind][]string{}
	for _, app := range slices.Sorted(maps.Keys(req.promotion.Builds)) {
		kind := s.routerOf(routedBy(app)).Kind()
		byRouter[kind] = append(byRouter[kind], app)
	}
	routers := make([]appRouter, 0, len(byRouter))
	kinds := slices.Sorted(maps.Keys(byRouter))
	for _, kind := range kinds {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return nil, err
		}
		routers = append(routers, appRouter{stack: routed, apps: byRouter[kind]})
	}
	dropped, err := promote(ctx, s.ledger, req, routers, progress)
	for i, kind := range kinds {
		err = errors.Join(err, s.adopt(kind, routers[i].stack))
	}
	if err != nil {
		return dropped, err
	}
	s.purgePromotedHostnames(ctx, req.pointer, progress)
	return dropped, nil
}

func (s *sharedStack) purgePromotedHostnames(ctx context.Context, pointer string, progress progress.Log) {
	purge := s.front.Hooks().PurgeHostnames
	bound := s.edgeStack().State().Bound
	if purge == nil || !router.IsDefaultPointer(pointer) || len(bound) == 0 {
		return
	}
	if err := purge(ctx, bound); err != nil {
		progress.Warn(fmt.Sprintf("%s may keep answering %s from what it cached of the release this promote replaced until that cache expires: purging it failed: %v",
			describeFront(s.front.Kind()), strings.Join(bound, ", "), err))
	}
}

func (s *sharedStack) removePointer(ctx context.Context, pointer string, progress progress.Log) (router.PruneResult, error) {
	for _, kind := range s.listRouterKinds() {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return router.PruneResult{}, err
		}
		if err := errors.Join(routed.RemovePointer(ctx, pointer, progress), s.adopt(kind, routed)); err != nil {
			return router.PruneResult{}, err
		}
	}
	return s.ledger.RemovePointer(ctx, pointer)
}

func (s *sharedStack) destroy(ctx context.Context) error {
	for _, kind := range s.listRouterKinds() {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return err
		}
		if err := errors.Join(routed.Destroy(ctx), s.adopt(kind, routed)); err != nil {
			return err
		}
	}
	if err := s.edgeStack().Destroy(ctx); err != nil {
		return err
	}
	return s.ledger.Destroy(ctx)
}

func (s *sharedStack) routerState(kind router.Kind) router.StackState {
	if !s.keepsEdgeState(kind) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.states[kind]
	}
	state := s.edgeStack().State()
	return router.StackState{Slug: state.Slug, Tier: state.Tier}
}
