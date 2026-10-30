package bastion

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(c Clients, namespace, region string, release func(context.Context, environment.Tier, progress.Log) error) tier.Part {
	return tier.Part{
		Name:    Part,
		Summary: Summary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return describe(ctx, c, namespace, region, tier)
		},
		Inventory: func(at environment.Tier) []pricing.Item { return inventory(namespace, at) },
		Removal: &tier.Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return planDeletion(ctx, c, namespace, tier, "this run removes it; the next port forward adds it again")
			},
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				if err := Remove(ctx, c, namespace, tier); err != nil {
					return err
				}
				return release(ctx, tier, progress)
			},
		},
		Teardown: &tier.Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return planDeletion(ctx, c, namespace, tier, "the bootstrap it belongs to is being removed")
			},
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				if progress != nil {
					progress.Say(fmt.Sprintf("Removing the %s tier's bastion (ECS, IAM, EC2)", tier))
				}
				if err := Remove(ctx, c, namespace, tier); err != nil {
					return err
				}
				return release(ctx, tier, progress)
			},
		},
	}
}

func planDeletion(ctx context.Context, c Clients, namespace string, at environment.Tier, why string) (*provider.ChangeGroup, error) {
	resources, found, err := Describe(ctx, c, namespace, at)
	if err != nil || !found {
		return nil, err
	}
	return tier.Deletion(Part, why, resources), nil
}

func inventory(namespace string, at environment.Tier) []pricing.Item {
	return []pricing.Item{{Name: NameFor(namespace, at), Type: "aws_ecs_cluster", Properties: map[string]any{}}}
}

func describe(ctx context.Context, c Clients, namespace, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	resources, found, err := Describe(ctx, c, namespace, tier)
	if err != nil || !found {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      Part,
		Summary:   Summary,
		Current:   true,
		Resources: resources,
		Inventory: &provider.PartInventory{Vendor: awsports.Vendor, Region: region, Resources: inventory(namespace, tier)},
	}, true, nil
}
