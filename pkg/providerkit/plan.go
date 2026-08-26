package providerkit

import (
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

func WithoutDetail(reason string) string {
	if reason == "" {
		return DetailUnavailable
	}
	if strings.Contains(reason, DetailUnavailable) {
		return reason
	}
	return reason + "; " + DetailUnavailable
}
