package bootstrapplan

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func WithDefaultStackNames(described provider.BootstrapDescription, offered []provider.Part, name func(part string) string) provider.BootstrapDescription {
	planned := make(map[string]bool, len(described.Stacks))
	for _, stack := range described.Stacks {
		planned[stack.Part] = true
	}
	named := described
	named.Stacks = slices.Clone(described.Stacks)
	for _, part := range append([]string{""}, partNames(offered)...) {
		if planned[part] {
			continue
		}
		named.Stacks = append(named.Stacks, provider.BootstrapStack{Name: name(part), Part: part})
	}
	return named
}

func ChangeGroups(described provider.BootstrapDescription, offered []provider.Part, req provider.BootstrapRequest) []provider.ChangeGroup {
	current := make(map[string]provider.BootstrapStack, len(described.Stacks))
	for _, stack := range described.Stacks {
		current[stack.Part] = stack
	}

	groups := []provider.ChangeGroup{baselineGroup(described, current[""], req.Tier)}
	for _, name := range req.Parts {
		groups = append(groups, partGroup(current[name], name))
	}
	for _, name := range req.Remove {
		if slices.Contains(req.Parts, name) {
			continue
		}
		groups = append(groups, provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   stackName(current[name], name),
			Part:   name,
			Action: provider.ActionDelete,
		})
	}
	return groups
}

func baselineGroup(described provider.BootstrapDescription, stack provider.BootstrapStack, tier environment.Tier) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, string(tier)+" bootstrap")}
	group.Action, group.Reason = bootstrapStackAction(stack, described.Present)
	return group
}

func partGroup(stack provider.BootstrapStack, name string) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: stackName(stack, name), Part: name}
	group.Action, group.Reason = bootstrapStackAction(stack, true)
	return group
}

func bootstrapStackAction(stack provider.BootstrapStack, described bool) (provider.ChangeAction, string) {
	switch {
	case !described || !stack.Present:
		return provider.ActionCreate, ""
	case !stack.DigestCurrent:
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

func stackName(stack provider.BootstrapStack, fallback string) string {
	if stack.Name != "" {
		return stack.Name
	}
	return fallback
}
