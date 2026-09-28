package box

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type Router struct{ e *Edge }

func NewRouter(e *Edge) Router { return Router{e: e} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{ReachesContainers: true, AnswersHostnames: true}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %q edge needs a project slug; this stack has none", Kind)
	}
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	return routerStack{s: &stack{e: r.e, state: state.Edge}}, nil
}

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	return router.NewStackState(r.s.State())
}

func (r routerStack) Ledger() router.Ledger { return r.s.openLedger() }

func (r routerStack) Claim(context.Context, string, string) error { return nil }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	s := r.s
	promotion, pointer := flip.Promotion, flip.Pointer
	ready := make([]promotable, 0, len(promotion.Builds))
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		release, serves, err := s.readyRelease(ctx, app, pointer, promotion)
		if err != nil {
			return err
		}
		if serves {
			ready = append(ready, release)
		}
	}
	if err := s.openLedger().Promote(ctx, promotion, pointer, progress); err != nil {
		return err
	}
	err := s.serve(ctx, flip, ready, progress)
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		return err
	}
	if undo := s.openLedger().Unpromote(ctx, promotion.PromotionID, pointer); undo != nil {
		return errors.Join(err, fmt.Errorf("the ledger still points %s at %s, which this box never served: %w",
			named(pointer), promotion.PromotionID, undo))
	}
	return err
}

func (r routerStack) RemovePointer(ctx context.Context, pointer string, progress progress.Progress) error {
	s := r.s
	if err := s.e.machine.DisclaimPointer(ctx, s.surface(), named(pointer)); err != nil {
		return err
	}
	if err := s.applyOrigins(ctx); err != nil {
		progress.Warn(s.released("Preview "+pointer, err).Error())
	}
	return s.e.machine.UnroutePointer(ctx, s.surface(), named(pointer))
}

func (r routerStack) Destroy(context.Context) error { return nil }
