package apigateway

import (
	"context"
	"errors"
	"fmt"

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
		FlipBound:           router.FlipBound{Typical: propagationBound},
		SignsOriginForwards: true,
		ReachesFunctions:    true,
		AnswersHostnames:    true,
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

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	state := r.s.State()
	return router.NewStackState(state)
}

func (r routerStack) Ledger() router.Ledger { return &lazyLedger{s: r.s} }

func (r routerStack) Claim(context.Context, string, string) error { return nil }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	s, promotion, pointer := r.s, flip.Promotion, flip.Pointer
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	id, err := s.ensureAPI(ctx, c, pointer)
	if err != nil {
		return err
	}
	patch, err := s.stagePatch(ctx, c, promotion)
	if err != nil {
		return err
	}
	if err := moveStage(ctx, c, id, promotion.PromotionID, patch); err != nil {
		return router.Unserved{Err: err}
	}
	if err := s.routePreview(ctx, c, pointer, id); err != nil {
		return err
	}
	if err := s.openLedger(c).Promote(ctx, promotion, pointer, progress); err != nil {
		if restored := s.restage(ctx, c, pointer, id); restored != nil {
			return errors.Join(err, restored)
		}
		return router.Unserved{Err: err}
	}
	return nil
}

func (r routerStack) RemovePointer(ctx context.Context, pointer string, _ progress.Progress) error {
	s := r.s
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	if pointerOr(pointer) == router.DefaultPointer {
		return nil
	}
	if err := s.unroutePreview(ctx, c, pointer); err != nil {
		return err
	}
	id, found, err := findAPI(ctx, c, apiName(s.p.ns, s.slug(), s.tier(), pointer))
	if err != nil || !found {
		return err
	}
	return s.p.deletion().drain(ctx, c, []string{id})
}

func (r routerStack) Destroy(context.Context) error { return nil }
