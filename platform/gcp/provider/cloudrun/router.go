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
	return router.Facts{AddressesItself: true, ReachesFunctions: true, ReachesContainers: true}
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

func (r Router) ProjectRemovals(edge.ProjectScope) []edge.PlanGroup { return nil }

func (r Router) ClaimPreviewEntry(context.Context, router.Claim) (edge.Origin, error) {
	return edge.Origin{}, nil
}

func (r Router) DisclaimPreviewEntry(context.Context, string) error { return nil }

func (r Router) PreviewEntryRemovals(string) []edge.PlanGroup { return nil }

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	return router.NewStackState(r.s.State())
}

func (r routerStack) Claim(context.Context, router.Claim) (edge.Origin, error) {
	return edge.Origin{}, unbindable("a domain")
}

func (r routerStack) Disclaim(context.Context, string) error { return nil }

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	return pin.Flip(ctx, r.s.e.pins, flip, progress)
}

func (r routerStack) RemovePointer(context.Context, string, progress.Progress) error { return nil }

func (r routerStack) Destroy(context.Context) error { return nil }
