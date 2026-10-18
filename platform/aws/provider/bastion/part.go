package bastion

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(c Clients, region string) tier.Part {
	return tier.Part{
		Name:    Part,
		Summary: Summary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return describe(ctx, c, region, tier)
		},
		Removal: &tier.Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				resources, found, err := Describe(ctx, c, tier)
				if err != nil || !found {
					return nil, err
				}
				return deletion(resources), nil
			},
			Remove: func(ctx context.Context, tier environment.Tier, _ progress.Log) error {
				return Remove(ctx, c, tier)
			},
		},
		Teardown: &tier.Removal{
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				if progress != nil {
					progress.Say(fmt.Sprintf("Removing the %s tier's bastion (ECS, IAM, EC2)", tier))
				}
				return Remove(ctx, c, tier)
			},
		},
	}
}

func deletion(resources []provider.PartResource) *provider.ChangeGroup {
	return tier.Deletion(Part, "this run removes it; the next port forward adds it again", resources)
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
		Shape: &provider.PartShape{Vendor: component.Vendor, Region: region, Resources: []pricing.Shaped{
			{Name: NameFor(tier), Type: "aws_ecs_cluster", Properties: map[string]any{}},
		}},
	}, true, nil
}
