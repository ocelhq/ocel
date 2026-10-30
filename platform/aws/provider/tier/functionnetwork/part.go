package functionnetwork

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
		RefuseInUse:   Shared.RefuseInUse,
		Summary:       sharedparts.FunctionNetworkSummary,
		Inventory:     sharedparts.FunctionNetworkInventory,
		RemovalReason: "this run removes it; the next deploy of a serverless app that reaches a postgres or kv Ocel provisions adds it again",
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return sharedparts.ReadFunctionNetworkPart(ctx, store, region, tier)
		},
		Remove: stacks.Remove,
	})
}
