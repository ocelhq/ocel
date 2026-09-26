package provider

import (
	"strings"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
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
	ActionAdopt             ChangeAction = "adopt"
)

const (
	StackGroupKind     = "stack"
	EdgeGroupKind      = edge.EdgeGroupKind
	ParameterGroupKind = "parameters"

	DetailUnavailable = "resource-level detail unavailable"

	ReasonCurrent = "already current"
)

func ValidChangeAction(action ChangeAction) bool {
	switch action {
	case ActionCreate, ActionUpdate, ActionReplace, ActionDelete, ActionDisableThenDelete, ActionKeep, ActionAdopt:
		return true
	default:
		return false
	}
}

func (a ChangeAction) Writes() bool { return a != ActionKeep && a != ActionAdopt }

type Plan struct {
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

func RollUp(changes []Change) (ChangeAction, string) {
	if len(changes) == 0 {
		return ActionUpdate, DetailUnavailable
	}
	creates, unwritten, deletes := 0, 0, 0
	for _, change := range changes {
		switch {
		case !change.Action.Writes():
			unwritten++
		case change.Action == ActionCreate:
			creates++
		case change.Action == ActionDelete || change.Action == ActionDisableThenDelete:
			deletes++
		}
	}
	switch {
	case len(changes) == unwritten:
		return ActionKeep, ReasonCurrent
	case len(changes) == creates:
		return ActionCreate, ""
	case len(changes) == deletes:
		return ActionDelete, ""
	default:
		return ActionUpdate, ""
	}
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

func KeepOrCreate(stands bool) ChangeAction {
	if stands {
		return ActionKeep
	}
	return ActionCreate
}

var actionProtos = map[ChangeAction]planv1.Change_Action{
	"":                      planv1.Change_ACTION_UNSPECIFIED,
	ActionCreate:            planv1.Change_ACTION_CREATE,
	ActionUpdate:            planv1.Change_ACTION_UPDATE,
	ActionReplace:           planv1.Change_ACTION_REPLACE,
	ActionDelete:            planv1.Change_ACTION_DELETE,
	ActionDisableThenDelete: planv1.Change_ACTION_DISABLE_THEN_DELETE,
	ActionKeep:              planv1.Change_ACTION_KEEP,
	ActionAdopt:             planv1.Change_ACTION_ADOPT,
}

var protoActions = invertActions(actionProtos)

func invertActions(actions map[ChangeAction]planv1.Change_Action) map[planv1.Change_Action]ChangeAction {
	inverted := make(map[planv1.Change_Action]ChangeAction, len(actions))
	for action, drawn := range actions {
		inverted[drawn] = action
	}
	return inverted
}

func ActionProto(action ChangeAction) planv1.Change_Action { return actionProtos[action] }

func ActionFromProto(drawn planv1.Change_Action) (ChangeAction, bool) {
	action, known := protoActions[drawn]
	return action, known
}

func RollUpProto(changes []*planv1.Change) planv1.Change_Action {
	actions := make([]Change, 0, len(changes))
	for _, change := range changes {
		actions = append(actions, Change{Action: protoActions[change.GetAction()]})
	}
	action, _ := RollUp(actions)
	return ActionProto(action)
}
