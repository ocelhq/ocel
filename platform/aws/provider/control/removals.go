package control

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (b Bootstrapper) PlanRemoval(ctx context.Context, class providerkit.Class) (providerkit.BootstrapPlan, error) {
	read, err := bootstrap.Read(ctx, b.CFN, string(class))
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	stacks, err := bootstrap.PlanRemoval(ctx, b.CFN, read)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	shared, err := bootstrap.PassphraseHeldBySibling(ctx, b.CFN, string(class))
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	params, err := bootstrap.PlanParameterRemoval(ctx,
		bootstrap.ParamAPIs{SSM: b.SSM, IAM: b.IAM}, string(class), shared)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	if len(params.Changes) > 0 {
		stacks = append(stacks, params)
	}

	plan := providerkit.BootstrapPlan{Groups: providerkit.Vendored(groupVendor, stacks)}
	fronts, err := b.standingEdges(ctx, class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	for _, front := range fronts {
		group, err := b.removedEdgeGroup(ctx, class, front)
		if err != nil {
			return providerkit.BootstrapPlan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
}

func (b Bootstrapper) standingEdges(ctx context.Context, class providerkit.Class) ([]edge.Edge, error) {
	fronts := []edge.Edge{b.Edge}
	for _, kind := range b.Edges.Supported() {
		if kind == b.Edge.Kind() {
			continue
		}
		standing, err := bootstrap.EdgeStanding(ctx, b.SSM, string(class), kind)
		if err != nil {
			return nil, err
		}
		if !standing {
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

func (b Bootstrapper) removedEdgeGroup(ctx context.Context, class providerkit.Class, front edge.Edge) (*providerkit.ChangeGroup, error) {
	remover, ok := front.(edge.BootstrapRemover)
	if !ok {
		return nil, nil
	}
	planned, err := remover.PlanRemoveBootstrap(ctx, edge.Class(class))
	if err != nil {
		return nil, fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", front.Kind(), err)
	}
	if len(planned) == 0 {
		return nil, nil
	}
	changes, err := providerkit.EdgeChanges(front.Kind(), planned)
	if err != nil {
		return nil, err
	}
	return &providerkit.ChangeGroup{
		Kind:    providerkit.EdgeGroupKind,
		Name:    edge.EdgeGroupName(front.Kind()),
		Feature: providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), front.Kind()),
		Action:  providerkit.ActionDelete,
		Changes: changes,
	}, nil
}
