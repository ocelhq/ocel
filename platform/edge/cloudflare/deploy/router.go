package cloudflare

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

const moveAttempts = 5

type Router struct{ p *cloudflare }

func NewRouter(namespace string) Router { return Router{p: newCloudflare(namespace, Options{})} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		Supported:                   edge.AllNeeds(),
		Propagation:                 router.Propagation{Typical: recordTTL},
		CachesRecords:               true,
		SignsOriginForwards:         true,
		ReachesFunctions:            true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
		ServesPreviewDeployments:    true,
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

func (r Router) Hooks() router.Hooks {
	return router.Hooks{RouteTables: &router.RouteTableHooks{Store: r.storeRouteTable, Forget: r.forgetRouteTable}}
}

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	state := r.s.State()
	return router.NewStackState(state)
}

func (r routerStack) Claim(context.Context, router.Claim) (edge.Origin, error) {
	return edge.Origin{}, nil
}

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) MovePointer(ctx context.Context, move router.PointerMove, _ progress.Log) error {
	records, err := r.s.wrapEnvelopes(move.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	for attempt := range moveAttempts {
		if attempt > 0 {
			if err := waitBeforeRetry(ctx, storeRetryDelay(nil, attempt-1, retryJitter())); err != nil {
				return router.Unserved{Err: err}
			}
		}
		replaces, err := r.s.readServedPromotion(ctx, move.Pointer)
		if err != nil {
			return router.Unserved{Err: err}
		}
		if err := move.RefuseInactive(ctx); err != nil {
			return err
		}
		stale, err := r.s.movePointer(ctx, pointerMoveBody{
			Pointer:     move.Pointer,
			Replaces:    replaces,
			PromotionID: move.Promotion.PromotionID,
			Records:     records,
			Labels:      listPointerLabels(move.Hosts),
		})
		if err != nil || !stale {
			return err
		}
	}
	return router.Unserved{Err: fmt.Errorf("move the pointer to promotion %s: the releases store served another promotion on every one of %d attempts, so this move stopped rather than overwrite it", move.Promotion.PromotionID, moveAttempts)}
}

func listPointerLabels(hosts []edge.PreviewHost) []pointerLabel {
	labels := make([]pointerLabel, 0, len(hosts))
	for _, host := range hosts {
		labels = append(labels, pointerLabel{Label: host.ReadLabel(), App: host.App})
	}
	return labels
}

func (r routerStack) RemovePointer(ctx context.Context, removal router.PointerRemoval, _ progress.Log) error {
	return r.s.removePointerRecords(ctx, removal.Pointer)
}

func (r routerStack) Destroy(context.Context) error { return nil }
