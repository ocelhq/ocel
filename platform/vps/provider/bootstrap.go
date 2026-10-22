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
	served   func(context.Context, environment.Tier) error
	front    edge.Edge
}

func (e elevating) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	if err := e.Bootstrap.Apply(ctx, req, progress); err != nil || e.served == nil {
		return err
	}
	return e.served(ctx, req.Tier)
}

func (e elevating) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	plan, err := e.Bootstrap.PlanRemove(ctx, tier)
	if err != nil || e.front == nil {
		return plan, err
	}
	removals := e.front.Hooks().PlanRemoveBootstrap
	if removals == nil {
		return plan, nil
	}
	planned, err := removals(ctx, tier)
	if err != nil {
		return provider.Plan{}, fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", e.front.Kind(), err)
	}
	if len(planned) == 0 {
		return plan, nil
	}
	changes, err := bootstrapplan.EdgeChanges(e.front.Kind(), planned)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Groups = append(plan.Groups, provider.ChangeGroup{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(e.front.Kind()),
		Action:  provider.ActionDelete,
		Changes: changes,
	})
	return plan, nil
}

func (e elevating) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := e.elevated(ctx); err != nil {
		return err
	}
	if e.front != nil {
		if err := e.front.Teardown(ctx, tier); err != nil {
			return fmt.Errorf("tear the %s edge down for tier %s: %w", e.front.Kind(), tier, err)
		}
	}
	return e.Bootstrap.Remove(ctx, tier, progress)
}
