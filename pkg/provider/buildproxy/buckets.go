package buildproxy

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
)

func ListBucketNames(grant provider.BindingGrant) []string {
	var names []string
	for _, binding := range grant.Bindings {
		if name := binding.Properties[provider.PropertyBucket]; !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}
