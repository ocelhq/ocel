package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func RuntimeLayersPart(layers account.LayerVersionsAPI, store account.RecordStore, region string, read func(context.Context, environment.Tier) (account.Reading, error)) tier.Part {
	return tier.Part{
		Name:    account.RuntimeLayersPart,
		Summary: account.RuntimeLayersSummary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			reading, err := read(ctx, tier)
			if err != nil || !reading.Deployed.Present {
				return provider.BootstrapPart{}, false, err
			}
			part, found, err := account.ReadRuntimeLayersPart(ctx, layers, store(reading.Deployed.StateTable), tier)
			if err != nil || !found {
				return provider.BootstrapPart{}, false, err
			}
			part.Shape = &provider.PartShape{Vendor: string(groupVendor), Region: region, Resources: account.ShapeRuntimeLayers()}
			return part, true, nil
		},
	}
}
