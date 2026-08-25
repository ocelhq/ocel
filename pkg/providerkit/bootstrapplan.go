package providerkit

import (
	"slices"
	"strings"
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
	StackGroupKind = "stack"

	DetailUnavailable = "resource-level detail unavailable"
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

func DeriveGroups(described Bootstrap, catalogue []Feature, req BootstrapRequest) []ChangeGroup {
	standing := make(map[string]BootstrapStack, len(described.Stacks))
	for _, stack := range described.Stacks {
		standing[stack.Feature] = stack
	}

	groups := []ChangeGroup{baselineGroup(described, standing[""], req.Class)}
	for _, name := range req.Features {
		groups = append(groups, featureGroup(standing[name], name))
	}
	for _, name := range req.Drop {
		if slices.Contains(req.Features, name) {
			continue
		}
		groups = append(groups, ChangeGroup{
			Kind:    StackGroupKind,
			Name:    stackName(standing[name], name),
			Feature: name,
			Action:  ActionDelete,
			Reason:  name + " leaves the set",
		})
	}
	return groups
}

func baselineGroup(described Bootstrap, stack BootstrapStack, class Class) ChangeGroup {
	group := ChangeGroup{Kind: StackGroupKind, Name: stackName(stack, string(class)+" bootstrap")}
	switch {
	case !described.Present || !stack.Present:
		group.Action, group.Reason = ActionCreate, "new bootstrap"
	case behind(stack):
		group.Action, group.Reason = ActionUpdate, "content is behind this build"
	default:
		group.Action, group.Reason = ActionKeep, "already current"
	}
	return group
}

func featureGroup(stack BootstrapStack, name string) ChangeGroup {
	group := ChangeGroup{Kind: StackGroupKind, Name: stackName(stack, name), Feature: name}
	switch {
	case !stack.Present:
		group.Action, group.Reason = ActionCreate, name+" joins the feature set"
	case behind(stack):
		group.Action, group.Reason = ActionUpdate, "content is behind this build"
	default:
		group.Action, group.Reason = ActionKeep, "already current"
	}
	return group
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
