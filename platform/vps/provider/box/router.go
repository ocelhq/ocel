package box

import (
	"context"
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

func (r routerStack) Claim(context.Context, string, string) error { return nil }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	s := r.s
	ready := make([]promotable, 0, len(flip.Records))
	for _, app := range slices.Sorted(maps.Keys(flip.Records)) {
		release, serves, err := s.readyRelease(ctx, flip.Pointer, flip.Promotion.PromotionID, flip.Records[app])
		if err != nil {
			return router.Unserved{Err: err}
		}
		if serves {
			ready = append(ready, release)
		}
	}
	return s.serve(ctx, flip, ready, progress)
}

func (r routerStack) RemovePointer(ctx context.Context, pointer string, progress progress.Progress) error {
	s := r.s
	if err := s.e.machine.DisclaimPointer(ctx, s.surface(), router.ResolvePointer(pointer)); err != nil {
		return err
	}
	if err := s.applyOrigins(ctx); err != nil {
		progress.Warn(s.released("Preview "+pointer, err).Error())
	}
	return s.e.machine.UnroutePointer(ctx, s.surface(), router.ResolvePointer(pointer))
}

func (r routerStack) Destroy(context.Context) error { return nil }
