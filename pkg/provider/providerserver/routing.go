package providerserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func sharedRouter(p provider.Provider, front edge.Edge) (router.Router, error) {
	return p.Routers().Open(router.Kind(front.Kind()))
}

type sharedStack struct {
	front  edge.Edge
	router router.Router
	stack  edge.EdgeStack
}

func (s *sharedStack) openRouter() (router.Stack, error) {
	state := s.stack.State()
	return s.router.Open(router.StackState{Slug: state.Slug, Tier: state.Tier, Edge: state})
}

func (s *sharedStack) adopt(routed router.Stack) error {
	reopened, err := s.front.Open(routed.State().Edge)
	if err != nil {
		return err
	}
	s.stack = reopened
	return nil
}

func (s *sharedStack) ledger() (router.Ledger, error) {
	routed, err := s.openRouter()
	if err != nil {
		return nil, err
	}
	return routed.Ledger(), nil
}

func (s *sharedStack) flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	routed, err := s.openRouter()
	if err != nil {
		return err
	}
	flipped := routed.Flip(ctx, flip, progress)
	return errors.Join(flipped, s.adopt(routed))
}

func (s *sharedStack) removePointer(ctx context.Context, pointer string, progress progress.Progress) (router.PruneResult, error) {
	routed, err := s.openRouter()
	if err != nil {
		return router.PruneResult{}, err
	}
	if err := errors.Join(routed.RemovePointer(ctx, pointer, progress), s.adopt(routed)); err != nil {
		return router.PruneResult{}, err
	}
	return routed.Ledger().RemovePointer(ctx, pointer)
}

func (s *sharedStack) destroyStack(ctx context.Context) error {
	routed, err := s.openRouter()
	if err != nil {
		return err
	}
	if err := errors.Join(routed.Destroy(ctx), s.adopt(routed)); err != nil {
		return err
	}
	return s.stack.Destroy(ctx)
}

func (s *sharedStack) routerState() router.StackState {
	state := s.stack.State()
	return router.StackState{Slug: state.Slug, Tier: state.Tier}
}

func pairApps(facts provider.Facts, front edge.Kind, apps []provider.AppEntry) (map[string]router.Kind, error) {
	paired := make(map[string]router.Kind, len(apps))
	for _, entry := range apps {
		compute := entry.Compute()
		if compute == "" {
			compute = provider.ComputeServerless
		}
		kind, found := facts.PairedRouter(front, compute)
		if !found {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %s runs as %s compute, and the %q edge serves no %s app on this provider", entry.App, compute, front, compute)
		}
		if kind != router.Kind(front) {
			return nil, fmt.Errorf("this provider pairs the %q edge with another kind for %s apps, and a release is flipped only through the stack the edge keeps", front, compute)
		}
		paired[entry.App] = kind
	}
	return paired, nil
}
