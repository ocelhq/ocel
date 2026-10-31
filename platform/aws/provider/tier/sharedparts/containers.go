package sharedparts

import (
	"context"
	"slices"

	awscost "github.com/ocelhq/ocel/platform/aws/provider/cost"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	ContainersPart    = "containers"
	ContainersSummary = "the load balancer and cluster every container app in the tier runs behind"

	ContainersLogRetentionDays = 14

	kindLoadBalancer = "AWS::ElasticLoadBalancingV2::LoadBalancer"
)

type RaisedFronts struct {
	Private bool
	Public  bool
}

func ReadContainersPart(ctx context.Context, store records.Store, namespace, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	_, present, err := stackrecords.Read(ctx, store, tier, awsports.ContainersSlug, naming.InfraStack(string(tier)))
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := ListUsers(ctx, store, tier, awsports.ContainersSlug, "container infrastructure")
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, store, tier, ContainersPart)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      ContainersPart,
		Summary:   ContainersSummary,
		Current:   true,
		Resources: recorded.Resources,
		UsedBy:    users,
		Inventory: &provider.PartInventory{Vendor: awsports.Vendor, Region: region, Resources: ContainersInventory(tier, findContainerFronts(namespace, tier, recorded.Resources))},

		RetiresWithLastUser: true,
	}, true, nil
}

func findContainerFronts(namespace string, tier environment.Tier, resources []provider.PhysicalResource) RaisedFronts {
	names := func(id string) func(provider.PhysicalResource) bool {
		return func(r provider.PhysicalResource) bool { return r.Kind == kindLoadBalancer && r.ID == id }
	}
	return RaisedFronts{
		Private: slices.ContainsFunc(resources, names(awsports.ContainerClusterName(namespace, tier))),
		Public:  slices.ContainsFunc(resources, names(awsports.PublicBalancerName(namespace, tier))),
	}
}

func ContainersInventory(_ environment.Tier, fronts RaisedFronts) []pricing.Resource {
	inventory := []pricing.Resource{
		{Name: awsports.ContainersSlug, Type: awscost.TerraformECSCluster, Properties: map[string]any{}},
	}
	if fronts.Private {
		inventory = append(inventory, pricing.Resource{Name: awsports.ContainersSlug, Type: awscost.TerraformLoadBalancer, Properties: map[string]any{"load_balancer_type": "application", "internal": true}})
	}
	inventory = append(inventory, pricing.Resource{Name: awsports.ContainersSlug, Type: awscost.TerraformLogGroup, Properties: map[string]any{"retention_in_days": ContainersLogRetentionDays}})
	if fronts.Public {
		public := naming.Join(naming.WordSeparator, awsports.ContainersSlug, "public")
		inventory = append(inventory,
			pricing.Resource{Name: public, Type: awscost.TerraformLoadBalancer, Properties: map[string]any{"load_balancer_type": "application", "internal": false}},
			pricing.Resource{Name: naming.Join(naming.WordSeparator, awsports.ContainersSlug, "liveness"), Type: awscost.TerraformLambdaFunction, Properties: map[string]any{
				"runtime":           awsports.LambdaNodeRuntime,
				"memory_size":       awsports.LambdaDefaultMemoryMB,
				"architectures":     []any{arch.X8664},
				"ephemeral_storage": map[string]any{"size": awsports.LambdaDefaultEphemeralMB},
			}})
	}
	return inventory
}
