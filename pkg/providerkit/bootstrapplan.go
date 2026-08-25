package providerkit

import (
	"fmt"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ChangeAction string

const (
	ActionCreate            ChangeAction = "create"
	ActionUpdate            ChangeAction = "update"
	ActionReplace           ChangeAction = "replace"
	ActionDelete            ChangeAction = "delete"
	ActionDisableThenDelete ChangeAction = "disable-then-delete"
	ActionKeep              ChangeAction = "keep"
)

const (
	StackGroupKind     = "stack"
	EdgeGroupKind      = edge.EdgeGroupKind
	ParameterGroupKind = "parameters"

	DetailUnavailable = "resource-level detail unavailable"

	reasonCurrent = "already current"
)

func ValidChangeAction(action ChangeAction) bool {
	switch action {
	case ActionCreate, ActionUpdate, ActionReplace, ActionDelete, ActionDisableThenDelete, ActionKeep:
		return true
	default:
		return false
	}
}

type BootstrapPlan struct {
	Groups []ChangeGroup
}

type ChangeGroup struct {
	Kind    string
	Name    string
	Feature string
	Action  ChangeAction
	Reason  string
	Slow    bool
	Changes []Change
}

type Change struct {
	Kind   string
	Name   string
	Action ChangeAction
	Reason string
	Slow   bool
}

func WithoutDetail(reason string) string {
	if reason == "" {
		return DetailUnavailable
	}
	if strings.Contains(reason, DetailUnavailable) {
		return reason
	}
	return reason + "; " + DetailUnavailable
}

func NameStacks(described Bootstrap, catalogue []Feature, name func(feature string) string) Bootstrap {
	held := make(map[string]bool, len(described.Stacks))
	for _, stack := range described.Stacks {
		held[stack.Feature] = true
	}
	named := described
	named.Stacks = slices.Clone(described.Stacks)
	for _, feature := range append([]string{""}, featureNames(catalogue)...) {
		if held[feature] {
			continue
		}
		named.Stacks = append(named.Stacks, BootstrapStack{Name: name(feature), Feature: feature})
	}
	return named
}

func DeriveGroups(described Bootstrap, catalogue []Feature, req BootstrapRequest) []ChangeGroup {
	standing := make(map[string]BootstrapStack, len(described.Stacks))
	for _, stack := range described.Stacks {
		standing[stack.Feature] = stack
	}

	groups := []ChangeGroup{baselineGroup(described, standing[""], req.Class)}
	for _, name := range req.Features {
		groups = append(groups, featureGroup(standing[name], name))
	}
	for _, name := range req.Remove {
		if slices.Contains(req.Features, name) {
			continue
		}
		groups = append(groups, ChangeGroup{
			Kind:    StackGroupKind,
			Name:    stackName(standing[name], name),
			Feature: name,
			Action:  ActionDelete,
		})
	}
	return groups
}

func baselineGroup(described Bootstrap, stack BootstrapStack, class Class) ChangeGroup {
	group := ChangeGroup{Kind: StackGroupKind, Name: stackName(stack, string(class)+" bootstrap")}
	switch {
	case !described.Present || !stack.Present:
		group.Action = ActionCreate
	case behind(stack):
		group.Action = ActionUpdate
	default:
		group.Action, group.Reason = ActionKeep, reasonCurrent
	}
	return group
}

func featureGroup(stack BootstrapStack, name string) ChangeGroup {
	group := ChangeGroup{Kind: StackGroupKind, Name: stackName(stack, name), Feature: name}
	switch {
	case !stack.Present:
		group.Action = ActionCreate
	case behind(stack):
		group.Action = ActionUpdate
	default:
		group.Action, group.Reason = ActionKeep, reasonCurrent
	}
	return group
}

func Vendored(vendor Vendor, groups []ChangeGroup) []ChangeGroup {
	named := slices.Clone(groups)
	for i := range named {
		named[i].Name = string(vendor) + "/" + named[i].Name
	}
	return named
}

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

func RollUp(changes []Change) (ChangeAction, string) {
	if len(changes) == 0 {
		return ActionUpdate, DetailUnavailable
	}
	creates, keeps, deletes := 0, 0, 0
	for _, change := range changes {
		switch change.Action {
		case ActionCreate:
			creates++
		case ActionKeep:
			keeps++
		case ActionDelete, ActionDisableThenDelete:
			deletes++
		}
	}
	switch {
	case len(changes) == keeps:
		return ActionKeep, reasonCurrent
	case len(changes) == creates:
		return ActionCreate, ""
	case len(changes) == deletes:
		return ActionDelete, ""
	default:
		return ActionUpdate, ""
	}
}

func FeatureNeedingEdge(catalogue []Feature, kind edge.Kind) string {
	for _, f := range catalogue {
		if slices.Contains(f.Needs, NeedsEdgePrefix+string(kind)) {
			return f.Name
		}
	}
	return ""
}

func behind(stack BootstrapStack) bool {
	return !stack.DigestCurrent || int(stack.Schema) < BootstrapSchema
}

func stackName(stack BootstrapStack, fallback string) string {
	if stack.Name != "" {
		return stack.Name
	}
	return fallback
}
