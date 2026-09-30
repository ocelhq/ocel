package cloudrun

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

const RouterKind router.Kind = "cloud-run"

type Router struct{ e *Edge }

func NewRouter(e *Edge) Router { return Router{e: e} }

func (r Router) Kind() router.Kind { return RouterKind }

func (r Router) Facts() router.Facts {
	return router.Facts{Supported: edge.AllNeeds(), AddressesItself: true, ReachesFunctions: true, ReachesContainers: true}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	if spec.Slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"Cloud Run serves a project by its slug, and this stack names none")
	}
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	return routerStack{s: &stack{e: r.e, state: state.Edge}}, nil
}

func (r Router) Hooks() router.Hooks { return router.Hooks{} }

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	return router.NewStackState(r.s.State())
}

func (r routerStack) Claim(context.Context, router.Claim) (edge.Origin, error) {
	return edge.Origin{}, unbindable("a domain")
}

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	return pin.MovePointer(ctx, r.s.e.pins, move, progress)
}

func (r routerStack) RemovePointer(context.Context, string, progress.Log) error { return nil }

func (r routerStack) Destroy(context.Context) error { return nil }
