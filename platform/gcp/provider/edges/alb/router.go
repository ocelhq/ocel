package alb

import (
	"context"
	"errors"
	"maps"

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
		Supported:                   edge.AllNeeds(),
		ReachesFunctions:            true,
		ReachesContainers:           true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
		ServesPreviewDeployments:    true,
	}
}

func (r Router) Hooks() router.Hooks { return router.Hooks{} }

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
	if !s.recorded.LoadBalancer.provisioned() {
		balancer, err := s.e.readProvisionedLoadBalancer(ctx, s.state.Tier)
		if err != nil {
			return edge.Origin{}, err
		}
		s.recorded.LoadBalancer = balancer
		s.keep()
	}
	if err := s.BindDomain(ctx, edge.DomainBinding{Hostname: claim.Hostname, Certificate: claim.Certificate, App: claim.App}); err != nil {
		return edge.Origin{}, err
	}
	return edge.Origin{Address: s.recorded.LoadBalancer.Address, Certified: true}, nil
}

func (r routerStack) Disclaim(ctx context.Context, hostname string) error {
	return r.s.UnbindDomain(ctx, hostname)
}

func (r routerStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	if err := r.moveTraffic(ctx, move, progress); err != nil {
		return err
	}
	if err := r.s.removeUnheldTags(ctx); err != nil && progress != nil {
		progress.Warn("Left a Cloud Run revision tag for the next move to remove: " + err.Error())
	}
	return nil
}

func (r routerStack) moveTraffic(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	releases, err := readReleaseTags(move.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	if err := r.s.shareImmutableFiles(ctx, move); err != nil {
		return router.Unserved{Err: err}
	}
	hosts, tags := maps.Clone(r.s.recorded.Hosts), cloneDeploymentTags(r.s.recorded.DeploymentTags)
	if err := r.s.routePreviewHosts(ctx, move, progress); err != nil {
		return err
	}
	if _, _, deployment := router.ParseDeploymentPointer(move.Pointer); deployment {
		return nil
	}
	pointers, err := pin.MovePointer(ctx, r.s.e.deps.Pins, r.s.recorded.Pointers, move, progress)
	r.s.recorded.Pointers = pointers
	r.s.keep()
	var unserved router.Unserved
	if errors.As(err, &unserved) {
		return router.Unserved{Err: errors.Join(err, r.s.restorePreviewHosts(ctx, hosts, tags))}
	}
	if err != nil {
		return err
	}
	if err := r.s.released(ctx, move.Records, progress); err != nil {
		return err
	}
	r.s.purgeReplaced(ctx, move, releases, progress)
	pin.WarmRevisions(ctx, r.s.warmThroughLoadBalancer, move.Records, progress)
	return nil
}

func (r routerStack) RemovePointer(ctx context.Context, removal router.PointerRemoval, _ progress.Log) error {
	if err := r.s.withdrawPointer(ctx, removal); err != nil {
		return err
	}
	kept, err := pin.ClosePointer(ctx, r.s.e.deps.Pins, r.s.recorded.Pointers, removal.Pointer)
	if err != nil {
		return err
	}
	r.s.recorded.Pointers = kept
	r.s.keep()
	r.s.forgetPointer(removal.Pointer)
	return r.s.removeUnheldTags(ctx)
}

func (r routerStack) Destroy(ctx context.Context) error {
	return r.s.Destroy(ctx)
}
