package providerkit

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func EdgeGroup(kind edge.Kind, feature string, planned []edge.PlanChange) (provider.ChangeGroup, error) {
	changes, err := EdgeChanges(kind, planned)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	group := provider.ChangeGroup{
		Kind:    provider.EdgeGroupKind,
		Name:    edge.EdgeGroupName(kind),
		Feature: feature,
		Changes: changes,
	}
	group.Action, group.Reason = provider.RollUp(group.Changes)
	return group, nil
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

func EdgeGroupOf(group edge.PlanGroup) (provider.ChangeGroup, error) {
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
