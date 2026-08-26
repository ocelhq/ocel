package providerkit

import (
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func EdgeGroup(kind edge.Kind, feature string, planned []edge.PlanChange) (ChangeGroup, error) {
	changes, err := EdgeChanges(kind, planned)
	if err != nil {
		return ChangeGroup{}, err
	}
	group := ChangeGroup{
		Kind:    EdgeGroupKind,
		Name:    edge.EdgeGroupName(kind),
		Feature: feature,
		Changes: changes,
	}
	group.Action, group.Reason = RollUp(group.Changes)
	return group, nil
}

func EdgeChanges(kind edge.Kind, planned []edge.PlanChange) ([]Change, error) {
	changes := make([]Change, 0, len(planned))
	for _, change := range planned {
		if !edge.ValidPlanAction(change.Action) {
			return nil, fmt.Errorf(
				"the %s edge plans %q on %s %q, and %q is not an action a plan can render",
				kind, change.Action, change.Kind, change.Name, change.Action)
		}
		changes = append(changes, Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: EdgeAction(change.Action),
			Reason: change.Reason,
			Slow:   change.Slow,
		})
	}
	return changes, nil
}

func EdgeGroupOf(group edge.PlanGroup) (ChangeGroup, error) {
	kind, _ := edge.EdgeGroupKindOf(group.Name)
	if !edge.ValidPlanAction(group.Action) {
		return ChangeGroup{}, fmt.Errorf(
			"the %s edge plans %q on the group %q, and %q is not an action a plan can render",
			kind, group.Action, group.Name, group.Action)
	}
	changes, err := EdgeChanges(kind, group.Changes)
	if err != nil {
		return ChangeGroup{}, err
	}
	converted := ChangeGroup{
		Kind:    group.Kind,
		Name:    group.Name,
		Feature: group.Feature,
		Action:  EdgeAction(group.Action),
		Reason:  group.Reason,
		Slow:    group.Slow,
	}
	if len(changes) > 0 {
		converted.Changes = changes
	}
	return converted, nil
}

func EdgeAction(action edge.PlanAction) ChangeAction {
	switch action {
	case edge.PlanCreate:
		return ActionCreate
	case edge.PlanUpdate:
		return ActionUpdate
	case edge.PlanDelete:
		return ActionDelete
	case edge.PlanDisableThenDelete:
		return ActionDisableThenDelete
	case edge.PlanKeep:
		return ActionKeep
	default:
		return ChangeAction(action)
	}
}
