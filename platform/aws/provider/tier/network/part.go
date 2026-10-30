package network

import (
	"context"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(stacks *Stacks, store records.Store, region string) tier.Part {
	return tier.Shared(tier.SharedSpec{
		Part:          Shared.Part,
		RefuseInUse:   stacks.RefuseInUse,
		Summary:       sharedparts.NetworkSummary,
		Inventory:     sharedparts.NetworkInventory,
		RemovalReason: "this run removes it; the next container app, VPC-attached function, database, cache or port forward adds it again",
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return sharedparts.ReadNetworkPart(ctx, store, region, tier)
		},
		Remove: stacks.Remove,
	})
}
