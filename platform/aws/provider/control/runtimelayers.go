package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func RuntimeLayersPart(layers bootstrap.LayerVersionsAPI, store bootstrap.RecordStore, region string, read func(context.Context, environment.Tier) (bootstrap.Reading, error)) tier.Part {
	return tier.Part{
		Name:    bootstrap.RuntimeLayersPart,
		Summary: bootstrap.RuntimeLayersSummary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			reading, err := read(ctx, tier)
			if err != nil || !reading.Deployed.Present {
				return provider.BootstrapPart{}, false, err
			}
			part, found, err := bootstrap.ReadRuntimeLayersPart(ctx, layers, store(reading.Deployed.StateTable), tier)
			if err != nil || !found {
				return provider.BootstrapPart{}, false, err
			}
			part.Shape = &provider.PartShape{Vendor: string(groupVendor), Region: region, Resources: bootstrap.ShapeRuntimeLayers()}
			return part, true, nil
		},
	}
}
