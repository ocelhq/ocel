package alb

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Router struct{ e *Edge }

func NewRouter(e *Edge) Router { return Router{e: e} }

func (r Router) Kind() router.Kind { return router.Kind(Kind) }

func (r Router) Facts() router.Facts {
	return router.Facts{
		RoutesPreviewsByLabel: true,
		ReachesFunctions:      true,
		ReachesContainers:     true,
		AnswersHostnames:      true,
	}
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
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %q edge serves a project by slug, and this stack names none", Kind)
	}
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	s := &stack{e: r.e, state: state.Edge}
	if err := s.state.Private.Into(&s.recorded); err != nil {
		return nil, err
	}
	return routerStack{s: s}, nil
}

type routerStack struct{ s *stack }

func (r routerStack) State() router.StackState {
	return router.NewStackState(r.s.State())
}

func (r routerStack) Claim(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	s := r.s
	fronted := s.e.frontFor(claim)
	var front Front
	var err error
	switch {
	case len(claim.ClientCertificates) > 0:
		front, err = fronted.trustClaim(ctx, s.state.Tier, claim.Hostname, claim.ClientCertificates)
	case !s.recorded.Front.provisioned():
		front, err = fronted.readProvisionedFront(ctx, s.state.Tier)
	default:
		front = s.recorded.Front
	}
	if err != nil {
		return edge.Origin{}, err
	}
	if front != s.recorded.Front {
		s.recorded.Front = front
		s.keep()
	}
	if err := s.BindDomain(ctx, edge.DomainBinding{Hostname: claim.Hostname, Certificate: claim.Certificate, App: claim.App}); err != nil {
		return edge.Origin{}, err
	}
	return edge.Origin{Address: s.recorded.Front.Address, Certified: true}, nil
}

func (r routerStack) Disclaim(ctx context.Context, hostname string) error {
	shielded := r.s.recorded.Front.Shielded
	if err := r.s.UnbindDomain(ctx, hostname); err != nil {
		return err
	}
	if !shielded {
		return nil
	}
	return r.s.e.Shielded().withdrawClaims(ctx, r.s.state.Tier, hostname)
}

func (r routerStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	if err := pin.Flip(ctx, r.s.e.deps.Pins, flip, progress); err != nil {
		return err
	}
	return r.s.released(ctx, flip.Records, progress)
}

func (r routerStack) RemovePointer(context.Context, string, progress.Progress) error { return nil }

func (r routerStack) Destroy(ctx context.Context) error {
	shielded, hostnames := r.s.recorded.Front.Shielded, slices.Sorted(maps.Keys(r.s.recorded.Hosts))
	if err := r.s.Destroy(ctx); err != nil {
		return err
	}
	if !shielded || len(hostnames) == 0 {
		return nil
	}
	return r.s.e.Shielded().withdrawClaims(ctx, r.s.state.Tier, hostnames...)
}
