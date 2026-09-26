package bootstrapplan

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func WithDefaultStackNames(described provider.BootstrapDescription, catalogue []provider.Feature, name func(feature string) string) provider.BootstrapDescription {
	featured := make(map[string]bool, len(described.Stacks))
	for _, stack := range described.Stacks {
		featured[stack.Feature] = true
	}
	named := described
	named.Stacks = slices.Clone(described.Stacks)
	for _, feature := range append([]string{""}, featureNames(catalogue)...) {
		if featured[feature] {
			continue
		}
		named.Stacks = append(named.Stacks, provider.BootstrapStack{Name: name(feature), Feature: feature})
	}
	return named
}

func ChangeGroups(described provider.BootstrapDescription, catalogue []provider.Feature, req provider.BootstrapRequest) []provider.ChangeGroup {
	current := make(map[string]provider.BootstrapStack, len(described.Stacks))
	for _, stack := range described.Stacks {
		current[stack.Feature] = stack
	}

	groups := []provider.ChangeGroup{baselineGroup(described, current[""], req.Class)}
	for _, name := range req.Features {
		groups = append(groups, featureGroup(current[name], name))
	}
	for _, name := range req.Remove {
		if slices.Contains(req.Features, name) {
			continue
		}
		groups = append(groups, provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    stackName(current[name], name),
			Feature: name,
			Action:  provider.ActionDelete,
		})
	}
	return groups
}

func baselineGroup(described provider.BootstrapDescription, stack provider.BootstrapStack, class edge.Class) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, string(class)+" bootstrap")}
	group.Action, group.Reason = bootstrapStackAction(stack, described.Present)
	return group
}

func featureGroup(stack provider.BootstrapStack, name string) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, name), Feature: name}
	group.Action, group.Reason = bootstrapStackAction(stack, true)
	return group
}

func bootstrapStackAction(stack provider.BootstrapStack, described bool) (provider.ChangeAction, string) {
	switch {
	case !described || !stack.Present:
		return provider.ActionCreate, ""
	case behind(stack):
		return provider.ActionUpdate, ""
	default:
		return provider.ActionKeep, provider.ReasonCurrent
	}
}

func PrefixWithVendor(vendor provider.Vendor, groups []provider.ChangeGroup) []provider.ChangeGroup {
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
