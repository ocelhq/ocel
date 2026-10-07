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
	kind, found := p.Facts().FindEdgeRouter(front)
	if !found {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this provider serves no project through %s", describeFront(front))
	}
	return kind, nil
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

type pairedRouter struct {
	router.Router
	read   func() router.StackState
	adopt  func(router.StackState) error
	record func() router.StackState
}

func openSharedStack(p provider.Provider, front edge.Edge, tier environment.Tier, slug string) (*sharedStack, error) {
	edgeKind, err := findEdgeRouter(p, front.Kind())
	if err != nil {
		return nil, err
	}
	s := &sharedStack{
		front:    front,
		edgeKind: edgeKind,
		routers:  map[router.Kind]pairedRouter{},
		states:   map[router.Kind]router.StackState{},
		ledger:   openProjectLedger(p, tier, slug),
	}
	for _, kind := range p.Facts().ListPairedRouters(front.Kind()) {
		opened, err := p.Routers().Open(kind)
		if err != nil {
			return nil, err
		}
		if p.Facts().IsForwarded(front.Kind(), kind) {
			s.routers[kind] = s.pairForwarded(kind, opened)
			continue
		}
		s.routers[kind] = s.pairOwn(opened)
	}
	return s, nil
}

func (s *sharedStack) pairOwn(opened router.Router) pairedRouter {
	return pairedRouter{
		Router: opened,
		read:   func() router.StackState { return router.NewStackState(s.edgeStack().State()) },
		adopt: func(state router.StackState) error {
			reopened, err := s.front.Open(state.Edge)
			if err != nil {
				return err
			}
			s.setEdgeStack(reopened)
			return nil
		},
		record: func() router.StackState {
			shared := s.edgeStack().State()
			return router.StackState{Slug: shared.Slug, Tier: shared.Tier}
		},
	}
}

func (s *sharedStack) pairForwarded(kind router.Kind, opened router.Router) pairedRouter {
	own := func() router.StackState {
		shared := s.edgeStack().State()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.states[kind].WithSpec(router.StackSpec{Tier: shared.Tier, Slug: shared.Slug})
	}
	return pairedRouter{
		Router: opened,
		read:   own,
		adopt: func(state router.StackState) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.states[kind] = state
			return nil
		},
		record: own,
	}
}

type sharedStack struct {
	front    edge.Edge
	edgeKind router.Kind
	routers  map[router.Kind]pairedRouter
	ledger   projectLedger
	mu       sync.Mutex
	current  edge.EdgeStack
	states   map[router.Kind]router.StackState
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

func (s *sharedStack) edgeRouter() router.Router { return s.routers[s.edgeKind] }

func (s *sharedStack) findRouter(kind router.Kind) (pairedRouter, error) {
	paired, found := s.routers[kind]
	if !found {
		return pairedRouter{}, fmt.Errorf("no router of kind %q pairs with %s", kind, describeFront(s.front.Kind()))
	}
	return paired, nil
}

func (s *sharedStack) findRouterOrigin(kind router.Kind) (*router.OriginHooks, error) {
	paired, err := s.findRouter(kind)
	if err != nil {
		return nil, err
	}
	return routerOriginBehind(s.front, paired), nil
}

func (s *sharedStack) restoreRouterStates(recorded stackrecords.EdgeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.states, recorded.Routers)
}

func (s *sharedStack) listRouterKinds() []router.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := []router.Kind{s.edgeKind}
	for _, kind := range slices.Sorted(maps.Keys(s.states)) {
		if _, paired := s.routers[kind]; paired && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (s *sharedStack) openRouterStack(kind router.Kind) (router.Stack, error) {
	paired, err := s.findRouter(kind)
	if err != nil {
		return nil, err
	}
	return paired.Open(paired.read())
}

func (s *sharedStack) adopt(kind router.Kind, routed router.Stack) error {
	paired, err := s.findRouter(kind)
	if err != nil {
		return err
	}
	return paired.adopt(routed.State())
}

func (s *sharedStack) readSlowestPropagation(kinds []router.Kind) (router.Propagation, error) {
	slowest := s.edgeRouter().Facts().Propagation
	for _, kind := range kinds {
		paired, err := s.findRouter(kind)
		if err != nil {
			return router.Propagation{}, err
		}
		if propagation := paired.Facts().Propagation; propagation.Typical > slowest.Typical {
			slowest = propagation
		}
	}
	return slowest, nil
}

func (s *sharedStack) promoteApps(ctx context.Context, req promoteRequest, routedBy func(app string) (router.Kind, error), progress progress.Log) ([]ledger.RecordedPromotion, error) {
	byRouter := map[router.Kind][]string{}
	for _, app := range slices.Sorted(maps.Keys(req.promotion.Releases)) {
		kind, err := routedBy(app)
		if err != nil {
			return nil, err
		}
		byRouter[kind] = append(byRouter[kind], app)
	}
	superseded := groupHostsByRouter(req.superseded, slices.Collect(maps.Keys(byRouter)), routedBy)
	routers := make([]appRouter, 0, len(byRouter))
	kinds := slices.Sorted(maps.Keys(byRouter))
	for _, kind := range kinds {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return nil, err
		}
		routers = append(routers, appRouter{stack: routed, apps: byRouter[kind], hosts: listAppHosts(req.hosts, byRouter[kind]), previous: listAppHosts(req.previous, byRouter[kind]), superseded: superseded[kind]})
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

func listAppHosts(hosts []edge.PreviewHost, apps []string) []edge.PreviewHost {
	names := normalizeAppNames(apps)
	var kept []edge.PreviewHost
	for _, host := range hosts {
		if slices.Contains(names, host.App) {
			kept = append(kept, host)
		}
	}
	return kept
}

func groupHostsByRouter(hosts []edge.PreviewHost, kinds []router.Kind, routedBy func(app string) (router.Kind, error)) map[router.Kind][]edge.PreviewHost {
	grouped := map[router.Kind][]edge.PreviewHost{}
	for _, host := range hosts {
		kind, err := routedBy(host.App)
		if err == nil && slices.Contains(kinds, kind) {
			grouped[kind] = append(grouped[kind], host)
			continue
		}
		for _, kind := range kinds {
			grouped[kind] = append(grouped[kind], host)
		}
	}
	return grouped
}

func (s *sharedStack) serveDeployment(ctx context.Context, pointer string, promotion router.Promotion, routedBy func(app string) (router.Kind, error), progress progress.Log) error {
	if len(promotion.Hosts) == 0 {
		return nil
	}
	byRouter := map[router.Kind][]string{}
	for _, app := range slices.Sorted(maps.Keys(promotion.Releases)) {
		kind, err := routedBy(app)
		if err != nil {
			return err
		}
		byRouter[kind] = append(byRouter[kind], app)
	}
	deployment := router.FormatDeploymentPointer(pointer, promotion.PromotionID)
	stillKept := newStillKept(s.ledger, pointer, promotion.PromotionID)
	var errs []error
	for _, kind := range slices.Sorted(maps.Keys(byRouter)) {
		hosts := listAppHosts(promotion.Hosts, byRouter[kind])
		if len(hosts) == 0 {
			continue
		}
		errs = append(errs, s.serveDeploymentOn(ctx, kind, deployment, promotion, byRouter[kind], hosts, stillKept, progress))
	}
	return errors.Join(errs...)
}

func (s *sharedStack) serveDeploymentOn(ctx context.Context, kind router.Kind, deployment string, promotion router.Promotion, apps []string, hosts []edge.PreviewHost, stillKept router.StillActive, progress progress.Log) error {
	routed, err := s.openRouterStack(kind)
	if err != nil {
		return err
	}
	records, err := s.ledger.readRecords(ctx, promotion, apps)
	if err != nil {
		return err
	}
	moved := routed.MovePointer(ctx, router.PointerMove{
		Pointer:     deployment,
		Promotion:   promotion,
		Records:     records,
		Hosts:       hosts,
		StillActive: stillKept,
	}, progress)
	if moved == nil {
		if dropped := stillKept(ctx); dropped != nil {
			moved = errors.Join(dropped, routed.RemovePointer(ctx, router.PointerRemoval{Pointer: deployment, Hosts: hosts}, progress))
		}
	}
	return errors.Join(moved, s.adopt(kind, routed))
}

func (s *sharedStack) removeReleases(ctx context.Context, deployments []router.PointerRemoval, progress progress.Log) error {
	if len(deployments) == 0 {
		return nil
	}
	var errs []error
	for _, kind := range s.listRouterKinds() {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return err
		}
		for _, deployment := range deployments {
			errs = append(errs, routed.RemovePointer(ctx, deployment, progress))
		}
		errs = append(errs, s.adopt(kind, routed))
	}
	return errors.Join(errs...)
}

func (s *sharedStack) forgetRemovedReleases(ctx context.Context, pointer string, deployments []router.PointerRemoval) error {
	removed := make([]string, 0, len(deployments))
	for _, deployment := range deployments {
		removed = append(removed, deployment.Pointer)
	}
	if len(removed) == 0 {
		return nil
	}
	return s.ledger.ForgetPendingRemovals(ctx, pointer, removed)
}

func (s *sharedStack) removePointer(ctx context.Context, removal router.PointerRemoval, progress progress.Log) (router.PruneResult, error) {
	recorded, err := s.ledger.Read(ctx, removal.Pointer)
	if err != nil {
		return router.PruneResult{}, err
	}
	for _, kind := range s.listRouterKinds() {
		routed, err := s.openRouterStack(kind)
		if err != nil {
			return router.PruneResult{}, err
		}
		if err := errors.Join(routed.RemovePointer(ctx, removal, progress), s.adopt(kind, routed)); err != nil {
			return router.PruneResult{}, err
		}
	}
	if err := s.removeReleases(ctx, recorded.ListReleaseRemovals(), progress); err != nil {
		return router.PruneResult{}, err
	}
	return s.ledger.RemovePointer(ctx, removal.Pointer)
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

func refuseRoutersRecordedBeforeTheyWereForwarded(p provider.Provider, front edge.Kind, recorded stackrecords.EdgeState) error {
	facts := p.Facts()
	for _, app := range slices.Sorted(maps.Keys(recorded.Apps)) {
		kind := recorded.Apps[app]
		state := recorded.Routers[kind]
		if !facts.IsForwarded(front, kind) || state.Slug == "" || state.Edge.Slug != "" {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s was deployed through the %s router, which kept its routing inside %s's own stack, and %s now forwards to it instead: "+
				"deploying now would leave that routing behind. Take this project's deployments down with `ocel destroy` using the release that made them, then deploy again",
			app, kind, describeFront(front), describeFront(front))
	}
	return nil
}
