package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func (b Bootstrap) edgeBootstrapStacks(ctx context.Context, tier environment.Tier, deployed account.Deployed) ([]provider.BootstrapStack, error) {
	var stacks []provider.BootstrapStack
	for _, kind := range account.EdgeKindsFor(deployed.Parts.Names()) {
		front, err := b.open(kind)
		if err != nil {
			return nil, err
		}
		describe := front.Hooks().DescribeBootstrap
		if describe == nil {
			continue
		}
		fronting := bootstrapplan.PartFrontingEdge(account.Parts(), kind)
		parts, err := describe(ctx, tier)
		if err != nil {
			stacks = append(stacks, provider.BootstrapStack{
				Name:      string(kind) + "/bootstrap",
				Part:      fronting,
				Present:   true,
				ReadError: err.Error(),
			})
			continue
		}
		for _, part := range parts {
			stacks = append(stacks, edgePartStack(kind, fronting, part))
		}
	}
	return stacks, nil
}

func edgePartStack(kind edge.Kind, fronting string, part edge.BootstrapPart) provider.BootstrapStack {
	return provider.BootstrapStack{
		Name:          string(kind) + "/" + part.Name,
		Part:          fronting,
		Present:       true,
		DigestCurrent: part.Current,
	}
}
