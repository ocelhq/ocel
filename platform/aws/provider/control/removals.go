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
	front, err := b.removedEdgeGroup(ctx, class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	if front != nil {
		plan.Groups = append(plan.Groups, *front)
	}
	return plan, nil
}

func (b Bootstrapper) removedEdgeGroup(ctx context.Context, class providerkit.Class) (*providerkit.ChangeGroup, error) {
	remover, ok := b.Edge.(edge.BootstrapRemover)
	if !ok {
		return nil, nil
	}
	planned, err := remover.PlanRemoveBootstrap(ctx, edge.Class(class))
	if err != nil {
		return nil, fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", b.Edge.Kind(), err)
	}
	if len(planned) == 0 {
		return nil, nil
	}
	changes, err := providerkit.EdgeChanges(b.Edge.Kind(), planned)
	if err != nil {
		return nil, err
	}
	return &providerkit.ChangeGroup{
		Kind:    providerkit.EdgeGroupKind,
		Name:    edge.EdgeGroupName(b.Edge.Kind()),
		Feature: providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), b.Edge.Kind()),
		Action:  providerkit.ActionDelete,
		Changes: changes,
	}, nil
}
