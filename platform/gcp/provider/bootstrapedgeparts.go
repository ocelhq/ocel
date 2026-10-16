package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (b bootstrap) edgeBootstrapStacks(ctx context.Context, read survey) ([]provider.BootstrapStack, error) {
	var stacks []provider.BootstrapStack
	err := b.eachFront(read.Stamp.Parts, func(fronting provider.Part, front edge.Edge) error {
		describe := front.Hooks().DescribeBootstrap
		if describe == nil {
			return nil
		}
		parts, err := describe(ctx, read.Tier)
		if err != nil {
			stacks = append(stacks, provider.BootstrapStack{
				Name:      string(front.Kind()) + "/bootstrap",
				Part:      fronting.Name,
				Present:   true,
				ReadError: err.Error(),
			})
			return nil
		}
		for _, part := range parts {
			stacks = append(stacks, provider.BootstrapStack{
				Name:          string(front.Kind()) + "/" + part.Name,
				Part:          fronting.Name,
				Present:       true,
				DigestCurrent: part.Current,
			})
		}
		return nil
	})
	return stacks, err
}
