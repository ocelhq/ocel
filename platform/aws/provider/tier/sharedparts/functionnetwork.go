package sharedparts

import (
	"context"
	"strconv"

	awscost "github.com/ocelhq/ocel/platform/aws/provider/cost"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	FunctionNetworkSlug = "ocel-function-network"

	FunctionNetworkPart    = "function-network"
	FunctionNetworkSummary = "a NAT gateway and elastic IP for each zone of the tier's network (one shared in preview) and the routes its private subnets take through them, " +
		"which every serverless app in the tier that reaches a postgres or kv Ocel provisions runs its functions behind"
)

func ReadFunctionNetworkPart(ctx context.Context, store records.Store, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, FunctionNetworkPart)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := ListUsers(ctx, store, tier, FunctionNetworkSlug, "function network")
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      FunctionNetworkPart,
		Summary:   FunctionNetworkSummary,
		Current:   recorded.State == stackrecords.PartReady,
		Resources: recorded.Resources,
		UsedBy:    users,
		Inventory: &provider.PartInventory{Vendor: awsports.Vendor, Region: region, Resources: FunctionNetworkInventory(tier)},

		RetiresWithLastUser: true,
	}, true, nil
}

func FunctionNetworkInventory(tier environment.Tier) []pricing.Resource {
	var inventory []pricing.Resource
	for n := range FunctionNetworkNATGateways(tier) {
		name := FunctionNetworkSlug + "-" + strconv.Itoa(n+1)
		inventory = append(inventory,
			pricing.Resource{Name: name, Type: awscost.TerraformNATGateway, Properties: map[string]any{}},
			pricing.Resource{Name: name, Type: awscost.TerraformElasticIP, Properties: map[string]any{}})
	}
	return inventory
}

func FunctionNetworkNATGateways(tier environment.Tier) int {
	if tier == environment.TierProduction {
		return NetworkZones
	}
	return 1
}
