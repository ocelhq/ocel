package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const Kind router.Kind = "elb"

type Router struct {
	open func(context.Context, environment.Tier) (Clients, error)
}

func NewRouter(open func(context.Context, environment.Tier) (Clients, error)) Router {
	return Router{open: open}
}

func (r Router) Kind() router.Kind { return Kind }

func (r Router) Facts() router.Facts {
	return router.Facts{
		Supported:                   []edge.Need{edge.NeedStreaming},
		ReachesContainers:           true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
	}
}

func (r Router) Hooks() router.Hooks {
	return router.Hooks{Origin: &router.OriginHooks{
		PlanProjectRemoval:      planProjectRemoval,
		ClaimPreviewEntry:       refusePreviewEntry,
		DisclaimPreviewEntry:    func(context.Context, string) error { return nil },
		PlanPreviewEntryRemoval: func(string) []edge.PlanGroup { return nil },
	}}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the load balancer routes a project's hostnames by its slug, and this stack names none")
	}
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	s := &stack{open: r.open, state: state.Edge}
	if err := state.Edge.Private.Into(&s.recorded); err != nil {
		return nil, err
	}
	return s, nil
}
