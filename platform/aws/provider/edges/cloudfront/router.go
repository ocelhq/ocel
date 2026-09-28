package cloudfront

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type Router struct{ p *cloudFront }

var _ router.Router = Router{}

func NewRouter(ns bootstrap.Namespace, open func(context.Context) (Clients, error)) Router {
	return Router{p: newCloudFront(ns, open)}
}

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		FlipBound:         router.FlipBound{Typical: propagationBound},
		ReachesFunctions:  true,
		ReachesContainers: true,
		AnswersHostnames:  true,
	}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, fmt.Errorf("the %q edge fronts a project by slug; this stack names none", Kind)
	}
	state := prior.Edge
	state.Slug, state.Tier = spec.Slug, spec.Tier
	return r.Open(router.StackState{Slug: spec.Slug, Tier: spec.Tier, Edge: state})
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
	return router.StackState{Slug: state.Slug, Tier: state.Tier, Edge: state}
}

func (r routerStack) Ledger() router.Ledger { return &lazyLedger{s: r.s} }

func (r routerStack) Claim(context.Context, string, string) error { return nil }

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	s := r.s
	c, err := s.clients(ctx)
	if err != nil {
		return err
	}
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	if err := s.publish(ctx, c, flip.Promotion, flip.Pointer); err != nil {
		return router.Unserved{Err: err}
	}
	if err := s.openLedger(c).Promote(ctx, flip.Promotion, flip.Pointer, progress); err != nil {
		if restored := s.republish(ctx, c, flip.Pointer); restored != nil {
			return errors.Join(err, restored)
		}
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
	host := s.previewHost(pointer)
	if host == "" {
		return nil
	}
	return s.routes(c).apply(ctx, nil, []string{host})
}

func (r routerStack) Destroy(context.Context) error { return nil }
