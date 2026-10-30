package control

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func RuntimeLayersPart(layers account.LayerTeardownAPI, store account.RecordStore, ns account.Namespace, region string, read func(context.Context, environment.Tier) (account.Reading, error)) tier.Part {
	return tier.Part{
		Name:      account.RuntimeLayersPart,
		Summary:   account.RuntimeLayersSummary,
		Inventory: func(environment.Tier) []pricing.Resource { return account.InventoryRuntimeLayers() },
		Teardown: &tier.Removal{
			Plan: func(ctx context.Context, at environment.Tier) (*provider.ChangeGroup, error) {
				reading, err := read(ctx, at)
				if err != nil || !reading.Deployed.Present {
					return nil, err
				}
				group, found := account.PlanRuntimeLayersRemoval(ctx, layers, store(reading.Deployed.StateTable), ns, at)
				if !found {
					return nil, nil
				}
				return &group, nil
			},
			Remove: func(ctx context.Context, at environment.Tier, log progress.Log) error {
				reading, err := read(ctx, at)
				if err != nil || !reading.Deployed.Present {
					return err
				}
				log.Say(fmt.Sprintf("Deleting the %s tier's runtime layers (Lambda)", at))
				return account.DeleteRuntimeLayers(ctx, layers, store(reading.Deployed.StateTable), at, log)
			},
		},
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			reading, err := read(ctx, tier)
			if err != nil || !reading.Deployed.Present {
				return provider.BootstrapPart{}, false, err
			}
			part, found, err := account.ReadRuntimeLayersPart(ctx, layers, store(reading.Deployed.StateTable), tier)
			if err != nil || !found {
				return provider.BootstrapPart{}, false, err
			}
			part.Inventory = &provider.PartInventory{Vendor: string(Vendor), Region: region, Resources: account.InventoryRuntimeLayers()}
			return part, true, nil
		},
	}
}
