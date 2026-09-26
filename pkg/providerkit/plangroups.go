package providerkit

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func NameStacks(described provider.BootstrapReading, catalogue []provider.Feature, name func(feature string) string) provider.BootstrapReading {
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
		named.Stacks = append(named.Stacks, provider.BootstrapStack{Name: name(feature), Feature: feature})
	}
	return named
}

func DeriveGroups(described provider.BootstrapReading, catalogue []provider.Feature, req provider.BootstrapRequest) []provider.ChangeGroup {
	standing := make(map[string]provider.BootstrapStack, len(described.Stacks))
	for _, stack := range described.Stacks {
		standing[stack.Feature] = stack
	}

	groups := []provider.ChangeGroup{baselineGroup(described, standing[""], req.Class)}
	for _, name := range req.Features {
		groups = append(groups, featureGroup(standing[name], name))
	}
	for _, name := range req.Remove {
		if slices.Contains(req.Features, name) {
			continue
		}
		groups = append(groups, provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    stackName(standing[name], name),
			Feature: name,
			Action:  provider.ActionDelete,
		})
	}
	return groups
}

func baselineGroup(described provider.BootstrapReading, stack provider.BootstrapStack, class edge.Class) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, string(class)+" bootstrap")}
	group.Action, group.Reason = standingAction(stack, described.Present)
	return group
}

func featureGroup(stack provider.BootstrapStack, name string) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, name), Feature: name}
	group.Action, group.Reason = standingAction(stack, true)
	return group
}

func standingAction(stack provider.BootstrapStack, holding bool) (provider.ChangeAction, string) {
	switch {
	case !holding || !stack.Present:
		return provider.ActionCreate, ""
	case behind(stack):
		return provider.ActionUpdate, ""
	default:
		return provider.ActionKeep, provider.ReasonCurrent
	}
}

func Vendored(vendor provider.Vendor, groups []provider.ChangeGroup) []provider.ChangeGroup {
	named := slices.Clone(groups)
	for i := range named {
		named[i].Name = string(vendor) + "/" + named[i].Name
	}
	return named
}

func FeatureNeedingEdge(catalogue []provider.Feature, kind edge.Kind) string {
	for _, f := range catalogue {
		if slices.Contains(f.Needs, provider.NeedsEdgePrefix+string(kind)) {
			return f.Name
		}
	}
	return ""
}

func behind(stack provider.BootstrapStack) bool {
	return !stack.DigestCurrent || int(stack.Schema) < provider.BootstrapSchema
}

func stackName(stack provider.BootstrapStack, fallback string) string {
	if stack.Name != "" {
		return stack.Name
	}
	return fallback
}
