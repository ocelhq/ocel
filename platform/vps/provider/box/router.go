package box

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type Router struct{ e *Edge }

func NewRouter(e *Edge) Router { return Router{e: e} }

func (r Router) Kind() router.Kind { return switchboard.RouterKind }

func (r Router) Facts() router.Facts {
	return router.Facts{Supported: edge.AllNeeds(), ReachesContainers: true, AnswersHostnames: true, StopsServingRemovedPointers: true, ServesPreviewDeployments: true}
}

func (r Router) Hooks() router.Hooks {
	return router.Hooks{Origin: &router.OriginHooks{
		PlanProjectRemoval:      r.planProjectRemoval,
		ClaimPreviewEntry:       r.claimPreviewEntry,
		DisclaimPreviewEntry:    r.disclaimPreviewEntry,
		PlanPreviewEntryRemoval: r.planPreviewEntryRemoval,
	}}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the box serves a project by its slug, and this stack names none")
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

func (r routerStack) Claim(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	return r.s.claimHostname(ctx, claim)
}

func (r routerStack) Disclaim(ctx context.Context, hostname string) error {
	if err := r.s.disclaimHostname(ctx, hostname); err != nil {
		return err
	}
	if err := r.s.e.releaseTunnel(ctx); err != nil {
		return err
	}
	if err := r.s.applyOrigins(ctx); err != nil {
		return progress.MarkWarning(r.s.released(hostname, err))
	}
	return nil
}

func (r routerStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	s := r.s
	ready := make([]promotable, 0, len(move.Records))
	for _, app := range slices.Sorted(maps.Keys(move.Records)) {
		release, serves, err := s.readyRelease(ctx, move.Pointer, move.Promotion.PromotionID, move.Records[app])
		if err != nil {
			return router.Unserved{Err: err}
		}
		if serves {
			ready = append(ready, release)
		}
	}
	return s.serve(ctx, move, ready, progress)
}

func (r routerStack) RemovePointer(ctx context.Context, removal router.PointerRemoval, progress progress.Log) error {
	s := r.s
	pointer := router.ResolvePointer(removal.Pointer)
	if err := s.e.machine.DisclaimPointer(ctx, s.surface(), pointer); err != nil {
		return err
	}
	if err := s.applyOrigins(ctx); err != nil {
		progress.Warn(s.released("Preview "+pointer, err).Error())
	}
	return s.e.machine.UnroutePointer(ctx, s.surface(), pointer)
}

func (r routerStack) Destroy(ctx context.Context) error { return r.s.Destroy(ctx) }
