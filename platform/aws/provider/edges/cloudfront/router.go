package cloudfront

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type Router struct{ p *cloudFront }

func NewRouter(ns bootstrap.Namespace, open func(context.Context) (Clients, error)) Router {
	return Router{p: newCloudFront(ns, open)}
}

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		FlipBound:                   router.FlipBound{Typical: propagationBound},
		ReachesFunctions:            true,
		ReachesContainers:           true,
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
	s := r.s
	c, err := s.clients(ctx)
	if err != nil {
		return router.Unserved{Err: err}
	}
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	if err := s.publishOn(ctx, c, flip.Promotion.PromotionID, flip.Records, s.servedHostnames(flip.Pointer), flip.RefuseInactive); err != nil {
		return router.Unserved{Err: err}
	}
	return nil
}

func (r routerStack) RemovePointer(ctx context.Context, pointer string, _ progress.Progress) error {
	s := r.s
	c, err := s.clients(ctx)
	if err != nil {
		return err
	}
	if !s.provisioned() {
		return nil
	}
	return s.routes(c).apply(ctx, nil, s.servedHostnames(pointer))
}

func (r routerStack) Destroy(context.Context) error { return nil }
