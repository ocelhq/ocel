package apigateway

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type Router struct{ p *apiGateway }

func NewRouter(ns bootstrap.Namespace, open func(context.Context) (Clients, error)) Router {
	return Router{p: newAPIGateway(ns, open)}
}

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		Supported:                   edge.AllNeeds(),
		Propagation:                 router.Propagation{Typical: propagationBound},
		SignsOriginForwards:         true,
		ReachesFunctions:            true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
	}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, fmt.Errorf("the %q edge fronts a project by slug; this stack names none", Kind)
	}
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	s := &stack{p: r.p, state: state.Edge}
	if err := state.Edge.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return routerStack{s: s}, nil
}

func (r Router) Hooks() router.Hooks { return router.Hooks{} }

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
	s, pointer := r.s, move.Pointer
	patch, err := stagePatch(move.Promotion.PromotionID, move.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return router.Unserved{Err: err}
	}
	stageWritten := false
	err = s.newStageLease(c, pointer, move.Promotion.PromotionID).hold(ctx, func(held *heldLease) error {
		if err := move.RefuseInactive(ctx); err != nil {
			return err
		}
		id, err := s.ensureAPI(ctx, c, pointer)
		if err != nil {
			return err
		}
		bounded, stop, err := held.renew(ctx)
		if err != nil {
			return err
		}
		defer stop()
		routes, err := routeStatic(bounded, c, s.spec(pointer), id, immutablePrefixes(move.Records))
		if err != nil {
			return err
		}
		published, err := stageVariable(bounded, c, id, routesVariable)
		if err != nil {
			return err
		}
		if routes.Reshaped || published != routes.Fingerprint {
			if err := publish(bounded, c, id); err != nil {
				return err
			}
		}
		patch := append(slices.Clone(patch), variablePatch(map[string]string{routesVariable: routes.Fingerprint})...)
		if err := moveStage(bounded, c, id, move.Promotion.PromotionID, patch); err != nil {
			return err
		}
		stageWritten = true
		return s.moveHostRules(ctx, c, move.Hosts, move.ListHostsToWithdraw(), id)
	})
	if err != nil && !stageWritten {
		return router.Unserved{Err: err}
	}
	return err
}

func (r routerStack) RemovePointer(ctx context.Context, removal router.PointerRemoval, _ progress.Log) error {
	s, pointer := r.s, removal.Pointer
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	if router.IsDefaultPointer(pointer) {
		return s.unsetStage(ctx, c)
	}
	if err := s.unroutePreview(ctx, c, removal.Hosts); err != nil {
		return err
	}
	id, found, err := findAPI(ctx, c, apiName(s.p.ns, s.slug(), s.tier(), pointer))
	if err != nil || !found {
		return err
	}
	return s.p.deletion().drain(ctx, c, []string{id})
}

func (r routerStack) Destroy(context.Context) error { return nil }
