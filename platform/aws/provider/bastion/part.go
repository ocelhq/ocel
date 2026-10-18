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

func NewPart(c Clients, region string) tier.Part {
	return tier.Part{
		Name:    Part,
		Summary: Summary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return describe(ctx, c, region, tier)
		},
		Shape: shape,
		Removal: &tier.Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return planDeletion(ctx, c, tier, "this run removes it; the next port forward adds it again")
			},
			Remove: func(ctx context.Context, tier environment.Tier, _ progress.Log) error {
				return Remove(ctx, c, tier)
			},
		},
		Teardown: &tier.Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return planDeletion(ctx, c, tier, "the bootstrap it belongs to is being removed")
			},
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				if progress != nil {
					progress.Say(fmt.Sprintf("Removing the %s tier's bastion (ECS, IAM, EC2)", tier))
				}
				return Remove(ctx, c, tier)
			},
		},
	}
}

func planDeletion(ctx context.Context, c Clients, at environment.Tier, why string) (*provider.ChangeGroup, error) {
	resources, found, err := Describe(ctx, c, at)
	if err != nil || !found {
		return nil, err
	}
	return tier.Deletion(Part, why, resources), nil
}

func shape(at environment.Tier) []pricing.Shaped {
	return []pricing.Shaped{{Name: NameFor(at), Type: "aws_ecs_cluster", Properties: map[string]any{}}}
}

func describe(ctx context.Context, c Clients, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	resources, found, err := Describe(ctx, c, tier)
	if err != nil || !found {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      Part,
		Summary:   Summary,
		Current:   true,
		Resources: resources,
		Shape:     &provider.PartShape{Vendor: awsports.Vendor, Region: region, Resources: shape(tier)},
	}, true, nil
}
