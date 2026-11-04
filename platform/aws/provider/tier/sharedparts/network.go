package sharedparts

import (
	"context"
	"fmt"

	awscost "github.com/ocelhq/ocel/platform/aws/provider/cost"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	NetworkSlug = "ocel-network"

	NetworkPart    = "network"
	NetworkSummary = "the VPC every container app, VPC-attached function, database, cache and bastion of the tier runs in: a public, a private and an isolated subnet in each of two zones, " +
		"an internet gateway, S3 and DynamoDB gateway endpoints, and the security group whatever reaches a database or cache joins"

	NetworkZones        = 2
	NetworkEndpointType = "Gateway"
)

var (
	NetworkEndpointServices = []string{"s3", "dynamodb"}

	networkSubnetRoles = []string{"public", "private", "isolated"}
)

func ReadNetworkPart(ctx context.Context, store records.Store, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, NetworkPart)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := ListUsers(ctx, store, tier, NetworkSlug, "network")
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      NetworkPart,
		Summary:   NetworkSummary,
		Current:   recorded.State == stackrecords.PartReady,
		Resources: recorded.Resources,
		UsedBy:    users,
		Inventory: &provider.PartInventory{Vendor: awsports.Vendor, Region: region, Resources: NetworkInventory(tier)},

		RetiresWithLastUser: true,
	}, true, nil
}

func NetworkInventory(tier environment.Tier) []pricing.Resource {
	name := NetworkSlug + "-" + string(tier)
	inventory := []pricing.Resource{
		{Name: name, Type: awscost.TerraformVPC, Properties: map[string]any{}},
		{Name: name, Type: awscost.TerraformInternetGateway, Properties: map[string]any{}},
	}
	for _, role := range networkSubnetRoles {
		for n := range NetworkZones {
			inventory = append(inventory, pricing.Resource{Name: naming.Join(naming.WordSeparator, name, role, fmt.Sprint(n+1)), Type: awscost.TerraformSubnet, Properties: map[string]any{}})
		}
	}
	for _, service := range NetworkEndpointServices {
		inventory = append(inventory, pricing.Resource{Name: naming.Join(naming.WordSeparator, name, service), Type: awscost.TerraformVPCEndpoint, Properties: map[string]any{"vpc_endpoint_type": NetworkEndpointType}})
	}
	return inventory
}
