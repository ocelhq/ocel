package bastion

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func Describe(ctx context.Context, c Clients, tier environment.Tier) ([]provider.PartResource, bool, error) {
	name := NameFor(tier)
	found, err := c.findCluster(ctx, name)
	if err != nil || !found {
		return nil, false, err
	}
	return []provider.PartResource{
		{Kind: "AWS::ECS::Cluster", ID: name},
		{Kind: "AWS::ECS::TaskDefinition", ID: name},
	}, true, nil
}
