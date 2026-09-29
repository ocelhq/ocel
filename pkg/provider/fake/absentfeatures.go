package fake

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (b *Bootstrap) DescribeAbsent(features ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.absent = slices.Clone(features)
}

func (b *Bootstrap) absentStacks(tier environment.Tier, applied []string) []provider.BootstrapStack {
	var stacks []provider.BootstrapStack
	for _, feature := range b.absent {
		if slices.Contains(applied, feature) {
			continue
		}
		stack := b.stack(tier, feature)
		stack.Present = false
		stacks = append(stacks, stack)
	}
	return stacks
}
