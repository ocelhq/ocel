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
)

func findPairedRouter(p provider.Provider, front edge.Kind) (router.Kind, error) {
	kinds := p.Facts().ListPairedRouters(front)
	switch {
	case len(kinds) == 0:
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this provider serves no project through %s", describeFront(front))
	case len(kinds) > 1:
		// TODO(#1363): a project whose apps pair with different routers opens one stack per router.
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this provider serves the apps behind %s in more than one way, and a project is served one way", describeFront(front))
	}
	return kinds[0], nil
}

func openPairedRouter(p provider.Provider, front edge.Kind) (router.Router, error) {
	kind, err := findPairedRouter(p, front)
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

func (s *sharedStack) routerOrigin() *router.OriginHooks {
	return routerOriginBehind(s.front, s.router)
}

func openSharedStack(p provider.Provider, front edge.Edge, tier environment.Tier, slug string) (*sharedStack, error) {
	paired, err := openPairedRouter(p, front.Kind())
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

func (s *sharedStack) promote(ctx context.Context, req promoteRequest, progress progress.Progress) ([]ledger.RecordedPromotion, error) {
	routed, err := s.openRouterStack()
	if err != nil {
		return nil, err
	}
	apps := slices.Sorted(maps.Keys(req.promotion.Builds))
	dropped, err := promote(ctx, s.ledger, req, []appRouter{{stack: routed, apps: apps}}, progress)
	if err := errors.Join(err, s.adopt(routed)); err != nil {
		return dropped, err
	}
	s.purgeFlipped(ctx, req.pointer, progress)
	return dropped, nil
}

func (s *sharedStack) purgeFlipped(ctx context.Context, pointer string, progress progress.Progress) {
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
