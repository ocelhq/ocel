package bootstrap

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func NameStacks(described providerkit.Bootstrap) providerkit.Bootstrap {
	target, err := bootstrapFor(string(described.Class))
	if err != nil {
		return described
	}
	held := make(map[string]bool, len(described.Stacks))
	for _, stack := range described.Stacks {
		held[stack.Feature] = true
	}
	named := described
	named.Stacks = slices.Clone(described.Stacks)
	if !held[""] {
		named.Stacks = append(named.Stacks, providerkit.BootstrapStack{Name: target.stackName})
	}
	for _, f := range featureRegistry {
		if held[f.name] {
			continue
		}
		named.Stacks = append(named.Stacks, providerkit.BootstrapStack{
			Name:    f.stackName(string(described.Class)),
			Feature: f.name,
		})
	}
	return named
}
