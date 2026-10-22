package bootstrapplan

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func EdgeGroup(kind edge.Kind, part string, planned []edge.PlanChange, origin ...provider.Change) (provider.ChangeGroup, error) {
	changes, err := EdgeChanges(kind, planned)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	group := provider.ChangeGroup{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(kind),
		Part:    part,
		Changes: append(changes, origin...),
	}
	group.Action, group.Reason = provider.RollUp(group.Changes)
	return group, nil
}

func PlanEdgeBootstrap(ctx context.Context, front edge.Edge, tier environment.Tier, part string, origin ...provider.Change) (*provider.ChangeGroup, error) {
	var planned []edge.PlanChange
	if plan := front.Hooks().PlanBootstrap; plan != nil {
		var err error
		if planned, err = plan(ctx, tier); err != nil {
			return nil, fmt.Errorf("plan the %s edge bootstrap: %w", front.Kind(), err)
		}
	}
	if len(planned) == 0 && len(origin) == 0 {
		return nil, nil
	}
	group, err := EdgeGroup(front.Kind(), part, planned, origin...)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func PlanEdgeRemoval(ctx context.Context, front edge.Edge, tier environment.Tier, part string, origin ...provider.Change) (*provider.ChangeGroup, error) {
	var planned []edge.PlanChange
	if plan := front.Hooks().PlanRemoveBootstrap; plan != nil {
		var err error
		if planned, err = plan(ctx, tier); err != nil {
			return nil, fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", front.Kind(), err)
		}
	}
	if len(planned) == 0 && len(origin) == 0 {
		return nil, nil
	}
	changes, err := EdgeChanges(front.Kind(), planned)
	if err != nil {
		return nil, err
	}
	return &provider.ChangeGroup{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(front.Kind()),
		Part:    part,
		Action:  provider.ActionDelete,
		Changes: append(changes, origin...),
	}, nil
}

func EdgeChanges(kind edge.Kind, planned []edge.PlanChange) ([]provider.Change, error) {
	changes := make([]provider.Change, 0, len(planned))
	for _, change := range planned {
		if !edge.ValidPlanAction(change.Action) {
			return nil, fmt.Errorf(
				"the %s edge plans %q on %s %q, and %q is not an action a plan can render",
				kind, change.Action, change.Kind, change.Name, change.Action)
		}
		changes = append(changes, provider.Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: EdgeAction(change.Action),
			Reason: change.Reason,
			Slow:   change.Slow,
		})
	}
	return changes, nil
}

func EdgeGroupFromPlanGroup(group edge.PlanGroup) (provider.ChangeGroup, error) {
	kind, _ := edge.EdgeGroupKindOf(group.Name)
	if !edge.ValidPlanAction(group.Action) {
		return provider.ChangeGroup{}, fmt.Errorf(
			"the %s edge plans %q on the group %q, and %q is not an action a plan can render",
			kind, group.Action, group.Name, group.Action)
	}
	changes, err := EdgeChanges(kind, group.Changes)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	converted := provider.ChangeGroup{
		Kind:   group.Kind,
		Name:   group.Name,
		Part:   group.Part,
		Action: EdgeAction(group.Action),
		Reason: group.Reason,
		Slow:   group.Slow,
	}
	if len(changes) > 0 {
		converted.Changes = changes
	}
	return converted, nil
}

func EdgeAction(action edge.PlanAction) provider.ChangeAction {
	switch action {
	case edge.PlanCreate:
		return provider.ActionCreate
	case edge.PlanUpdate:
		return provider.ActionUpdate
	case edge.PlanDelete:
		return provider.ActionDelete
	case edge.PlanDisableThenDelete:
		return provider.ActionDisableThenDelete
	case edge.PlanKeep:
		return provider.ActionKeep
	default:
		return provider.ChangeAction(action)
	}
}
