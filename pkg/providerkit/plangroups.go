package providerkit

import (
	"slices"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

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
	group.Action, group.Reason = standingAction(stack, described.Present)
	return group
}

func featureGroup(stack BootstrapStack, name string) ChangeGroup {
	group := ChangeGroup{Kind: StackGroupKind, Name: stackName(stack, name), Feature: name}
	group.Action, group.Reason = standingAction(stack, true)
	return group
}

func standingAction(stack BootstrapStack, holding bool) (ChangeAction, string) {
	switch {
	case !holding || !stack.Present:
		return ActionCreate, ""
	case behind(stack):
		return ActionUpdate, ""
	default:
		return ActionKeep, reasonCurrent
	}
}

func Vendored(vendor Vendor, groups []ChangeGroup) []ChangeGroup {
	named := slices.Clone(groups)
	for i := range named {
		named[i].Name = string(vendor) + "/" + named[i].Name
	}
	return named
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
