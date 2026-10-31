package control

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func (b Bootstrap) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	read, err := b.readAccount(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	stacks, err := account.PlanRemove(ctx, b.CFN, b.Layers, b.Records, read)
	if err != nil {
		return provider.Plan{}, err
	}
	params, err := account.PlanParameterRemoval(ctx, b.paramAPIs(), b.Namespace, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	if len(params.Changes) > 0 {
		stacks = append(stacks, params)
	}
	parts, err := b.planPartTeardown(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	stacks = append(parts, stacks...)

	plan := provider.Plan{Groups: bootstrapplan.PrefixWithVendor(Vendor, stacks)}
	fronts, err := b.installedEdges(ctx, tier, read.Deployed)
	if err != nil {
		return provider.Plan{}, err
	}
	for _, front := range fronts {
		group, err := bootstrapplan.PlanEdgeRemoval(ctx, front, tier, bootstrapplan.PartFrontingEdge(account.Parts(), front.Kind()))
		if err != nil {
			return provider.Plan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
}

func (b Bootstrap) planPartTeardown(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, part := range b.TierParts {
		if part.Teardown == nil || part.Teardown.Plan == nil {
			continue
		}
		group, err := part.Teardown.Plan(ctx, tier)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (b Bootstrap) installedEdges(ctx context.Context, tier environment.Tier, deployed account.Deployed) ([]edge.Edge, error) {
	partKinds := account.EdgeKindsFor(deployed.Parts.Names())
	fronts := []edge.Edge{b.Edge}
	for _, kind := range b.Kinds {
		if kind == b.Edge.Kind() {
			continue
		}
		installed := slices.Contains(partKinds, kind)
		if !installed {
			var err error
			if installed, err = account.EdgeInstalled(ctx, b.SSM, b.Namespace, tier, kind); err != nil {
				return nil, err
			}
		}
		if !installed {
			continue
		}
		front, err := b.Edges.Open(kind, nil)
		if err != nil {
			return nil, err
		}
		fronts = append(fronts, front)
	}
	return fronts, nil
}
