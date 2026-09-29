package cloudflare

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

const flipAttempts = 5

type Router struct{ p *cloudflare }

func NewRouter(namespace string) Router { return Router{p: newCloudflare(namespace)} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		FlipBound:                   router.FlipBound{Typical: recordTTL},
		CachesRecords:               true,
		SignsOriginForwards:         true,
		ReachesFunctions:            true,
		Dispatches:                  true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
	}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	s := &stack{p: r.p, state: state.Edge}
	if err := state.Edge.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return routerStack{s: s}, nil
}

func (r Router) ProjectRemovals(edge.ProjectScope) []edge.PlanGroup { return nil }

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	state := r.s.State()
	return router.NewStackState(state)
}

func (r routerStack) Claim(context.Context, router.Claim) (edge.Origin, error) {
	return edge.Origin{}, nil
}

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, _ progress.Progress) error {
	records, err := r.s.wrapEnvelopes(flip.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	for attempt := range flipAttempts {
		if attempt > 0 {
			if err := waitBeforeRetry(ctx, storeRetryDelay(nil, attempt-1, retryJitter())); err != nil {
				return router.Unserved{Err: err}
			}
		}
		replaces, err := r.s.readServedPromotion(ctx, flip.Pointer)
		if err != nil {
			return router.Unserved{Err: err}
		}
		if err := flip.RefuseInactive(ctx); err != nil {
			return err
		}
		stale, err := r.s.flip(ctx, flipBody{
			Pointer:     flip.Pointer,
			Replaces:    replaces,
			PromotionID: flip.Promotion.PromotionID,
			Records:     records,
		})
		if err != nil || !stale {
			return err
		}
	}
	return router.Unserved{Err: fmt.Errorf("flip promotion %s: the deployments store served another promotion on every one of %d attempts, so this flip stopped rather than overwrite it", flip.Promotion.PromotionID, flipAttempts)}
}

func (r routerStack) RemovePointer(ctx context.Context, pointer string, _ progress.Progress) error {
	return r.s.removePointerRecords(ctx, pointer)
}

func (r routerStack) Destroy(context.Context) error { return nil }
