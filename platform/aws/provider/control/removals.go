package control

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (b Bootstrap) PlanRemove(ctx context.Context, class edge.Class) (provider.Plan, error) {
	read, err := bootstrap.Read(ctx, b.CFN, b.Namespace, string(class))
	if err != nil {
		return provider.Plan{}, err
	}
	stacks, err := bootstrap.PlanRemove(ctx, b.CFN, read)
	if err != nil {
		return provider.Plan{}, err
	}
	shared, err := bootstrap.SiblingSharesPassphrase(ctx, b.CFN, b.Namespace, string(class))
	if err != nil {
		return provider.Plan{}, err
	}
	params, err := bootstrap.PlanParameterRemoval(ctx,
		b.paramAPIs(), b.Namespace, string(class), shared)
	if err != nil {
		return provider.Plan{}, err
	}
	if len(params.Changes) > 0 {
		stacks = append(stacks, params)
	}

	plan := provider.Plan{Groups: bootstrapplan.PrefixWithVendor(groupVendor, stacks)}
	fronts, err := b.installedEdges(ctx, class, read.Deployed)
	if err != nil {
		return provider.Plan{}, err
	}
	for _, front := range fronts {
		group, err := b.removedEdgeGroup(ctx, class, front)
		if err != nil {
			return provider.Plan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
}

func (b Bootstrap) installedEdges(ctx context.Context, class edge.Class, deployed bootstrap.Deployed) ([]edge.Edge, error) {
	featureKinds := bootstrap.EdgeKindsFor(deployed.Features.Names())
	fronts := []edge.Edge{b.Edge}
	for _, kind := range b.Kinds {
		if kind == b.Edge.Kind() {
			continue
		}
		installed := slices.Contains(featureKinds, kind)
		if !installed {
			var err error
			if installed, err = bootstrap.EdgeInstalled(ctx, b.SSM, b.Namespace, string(class), kind); err != nil {
				return nil, err
			}
		}
		if !installed {
			continue
		}
		front, err := b.Edges.Open(kind)
		if err != nil {
			return nil, err
		}
		fronts = append(fronts, front)
	}
	return fronts, nil
}

func (b Bootstrap) removedEdgeGroup(ctx context.Context, class edge.Class, front edge.Edge) (*provider.ChangeGroup, error) {
	plan := front.Hooks().PlanRemoveBootstrap
	if plan == nil {
		return nil, nil
	}
	planned, err := plan(ctx, class)
	if err != nil {
		return nil, fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", front.Kind(), err)
	}
	if len(planned) == 0 {
		return nil, nil
	}
	changes, err := bootstrapplan.EdgeChanges(front.Kind(), planned)
	if err != nil {
		return nil, err
	}
	return &provider.ChangeGroup{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(front.Kind()),
		Feature: bootstrapplan.FeatureNeedingEdge(bootstrap.Catalogue(), front.Kind()),
		Action:  provider.ActionDelete,
		Changes: changes,
	}, nil
}
