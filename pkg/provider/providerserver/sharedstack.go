package providerserver

import (
	"context"
	"errors"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func openSharedStack(p provider.Provider, front edge.Edge) (*sharedStack, error) {
	paired, err := p.Routers().Open(router.Kind(front.Kind()))
	if err != nil {
		return nil, err
	}
	return &sharedStack{front: front, router: paired}, nil
}

type sharedStack struct {
	front  edge.Edge
	router router.Router
	mu     sync.Mutex
	stack  edge.EdgeStack
}

func (s *sharedStack) openRouter() (router.Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.router.Open(router.NewStackState(s.stack.State()))
}

func (s *sharedStack) adopt(routed router.Stack) error {
	reopened, err := s.front.Open(routed.State().Edge)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stack = reopened
	return nil
}

func (s *sharedStack) useLedger(use func(router.Ledger) error) error {
	routed, err := s.openRouter()
	if err != nil {
		return err
	}
	return errors.Join(use(routed.Ledger()), s.adopt(routed))
}

func (s *sharedStack) putStaged(ctx context.Context, record router.DeploymentRecord) error {
	return s.useLedger(func(ledger router.Ledger) error { return ledger.PutStaged(ctx, record) })
}

func (s *sharedStack) readHistory(ctx context.Context, pointer string) ([]router.HistoryEntry, error) {
	var history []router.HistoryEntry
	err := s.useLedger(func(ledger router.Ledger) (err error) {
		history, err = ledger.History(ctx, pointer)
		return err
	})
	return history, err
}

func (s *sharedStack) prune(ctx context.Context, keepN int, pointer string) (router.PruneResult, error) {
	var pruned router.PruneResult
	err := s.useLedger(func(ledger router.Ledger) (err error) {
		pruned, err = ledger.Prune(ctx, keepN, pointer)
		return err
	})
	return pruned, err
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
	var removed router.PruneResult
	err = routed.RemovePointer(ctx, pointer, progress)
	if err == nil {
		removed, err = routed.Ledger().RemovePointer(ctx, pointer)
	}
	if err := errors.Join(err, s.adopt(routed)); err != nil {
		return router.PruneResult{}, err
	}
	return removed, nil
}

func (s *sharedStack) destroy(ctx context.Context) error {
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
