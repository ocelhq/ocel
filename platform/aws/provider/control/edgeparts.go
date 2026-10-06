package control

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func (b Bootstrap) edgeBootstrapStacks(ctx context.Context, tier environment.Tier, deployed bootstrap.Deployed) ([]provider.BootstrapStack, error) {
	var stacks []provider.BootstrapStack
	for _, kind := range bootstrap.EdgeKindsFor(deployed.Features.Names()) {
		front, err := b.open(kind)
		if err != nil {
			return nil, err
		}
		describe := front.Hooks().DescribeBootstrap
		if describe == nil {
			continue
		}
		parts, err := describe(ctx, tier)
		if err != nil {
			return nil, fmt.Errorf("describe the %s edge's bootstrap: %w", kind, err)
		}
		feature := bootstrapplan.FeatureNeedingEdge(bootstrap.Catalogue(), kind)
		for _, part := range parts {
			stacks = append(stacks, edgePartStack(kind, feature, part))
		}
	}
	return stacks, nil
}

func edgePartStack(kind edge.Kind, feature string, part edge.BootstrapPart) provider.BootstrapStack {
	return provider.BootstrapStack{
		Name:          string(kind) + "/" + part.Name,
		Feature:       feature,
		Present:       true,
		DigestCurrent: part.Current,
	}
}
