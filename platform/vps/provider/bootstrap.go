package vps

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

func (p *Provider) elevated(ctx context.Context) error {
	live, err := p.Session(ctx)
	if err != nil {
		return err
	}
	_, err = live.Preflight(ctx)
	return err
}

type elevating struct {
	provider.Bootstrap
	elevated func(context.Context) error
}

func (e elevating) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrap.Apply(ctx, req, progress)
}

func (e elevating) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	return e.Bootstrap.Remove(ctx, tier, progress)
}

type exposed struct {
	elevating
	refuseClosedPorts func(context.Context, environment.Tier) error
}

func (e exposed) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := e.elevating.Apply(ctx, req, progress); err != nil {
		return err
	}
	return e.refuseClosedPorts(ctx, req.Tier)
}

type fronted struct {
	elevating
	front  edge.Edge
	forget func(context.Context, environment.Tier) error
}

func (f fronted) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	plan, err := f.elevating.Plan(ctx, req)
	if err != nil {
		return plan, err
	}
	group, err := bootstrapplan.PlanEdgeBootstrap(ctx, f.front, req.Tier, "")
	if err != nil {
		return provider.Plan{}, err
	}
	if group != nil {
		plan.Groups = append(plan.Groups, *group)
	}
	return plan, nil
}

func (f fronted) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := f.elevating.Apply(ctx, req, progress); err != nil {
		return err
	}
	if _, err := f.front.Bootstrap(ctx, req.Tier); err != nil {
		return fmt.Errorf("bootstrap the %s edge for tier %s: %w", f.front.Kind(), req.Tier, err)
	}
	return nil
}

func (f fronted) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	plan, err := f.elevating.PlanRemove(ctx, tier)
	if err != nil {
		return plan, err
	}
	group, err := bootstrapplan.PlanEdgeRemoval(ctx, f.front, tier, "")
	if err != nil {
		return provider.Plan{}, err
	}
	if group != nil {
		plan.Groups = append(plan.Groups, *group)
	}
	return plan, nil
}

func (f fronted) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := f.elevated(ctx); err != nil {
		return err
	}
	if err := f.front.Teardown(ctx, tier); err != nil {
		return fmt.Errorf("tear the %s edge down for tier %s: %w", f.front.Kind(), tier, err)
	}
	if err := f.forget(ctx, tier); err != nil {
		return fmt.Errorf("remove what the %s edge keeps for tier %s: %w", f.front.Kind(), tier, err)
	}
	return f.Bootstrap.Remove(ctx, tier, progress)
}
