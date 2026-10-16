package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const (
	bastionPart    = "bastion"
	baseSummary    = "the database, buckets, keys, secrets, accounts and roles every deploy in this tier runs on"
	bastionSummary = "the service a port forward runs inside the tier's network"
)

func (b bootstrap) repairParts(ctx context.Context, tier environment.Tier) ([]string, error) {
	return stackrecords.RepairBootstrapParts(ctx, b.records, tier, []stackrecords.PartWork{b.envSourceSyncWork(tier, "")})
}

func (g bootstrapGate) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return nil, err
	}
	return b.DescribeParts(ctx, described)
}

func (b bootstrap) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	read, passed := described.VendorState.(survey)
	if !passed || read.Tier != described.Tier {
		var err error
		if read, err = b.survey(ctx, described.Tier); err != nil {
			return nil, err
		}
	}
	if !read.Present {
		return nil, nil
	}
	parts := b.surveyedParts(read, described.Stacks)
	sync, present, err := b.envSourceSyncPart(ctx, read.Tier)
	if err != nil {
		return nil, err
	}
	if present {
		parts = append(parts, sync)
	}
	service, err := bastion{clients: b.clients}.read(ctx, read.Tier)
	if err != nil {
		return nil, err
	}
	if service != nil {
		parts = append(parts, provider.BootstrapPart{
			Name:    bastionPart,
			Summary: bastionSummary,
			Current: true,
			Resources: []provider.PartResource{
				{Kind: string(KindService), ID: b.clients.Bastion(read.Tier)},
				{Kind: string(KindServiceAccount), ID: b.clients.BastionAccount(read.Tier)},
			},
			Shape: &provider.PartShape{Vendor: string(Vendor), Region: read.Region, Resources: []pricing.Shaped{{
				Name:       b.clients.Bastion(read.Tier),
				Type:       tfCloudRunService,
				Properties: serviceProperties(true, 0, revisionMemory, ingressInternal),
			}}},
		})
	}
	return parts, nil
}

func (b bootstrap) surveyedParts(read survey, stacks []provider.BootstrapStack) []provider.BootstrapPart {
	baseStack := read.Project + "/" + string(read.Tier)
	base := provider.BootstrapPart{
		Name:    provider.BasePart,
		Summary: baseSummary,
		Digest:  read.Stamp.Digest,
		Shape:   &provider.PartShape{Vendor: string(Vendor), Region: read.Region},
	}
	for _, item := range bootstrapItems(read.Names, read.Tier, read.Emulated) {
		base.Resources = append(base.Resources, provider.PartResource{Kind: string(item.Kind), ID: item.Name})
		if resourceType, priced := itemTypes[item.Kind]; priced {
			base.Shape.Resources = append(base.Shape.Resources, pricing.Shaped{Name: item.Name, Type: resourceType, Properties: itemProperties(item, read.Region)})
		}
	}
	parts := []provider.BootstrapPart{base}
	for _, stack := range stacks {
		switch stack.Name {
		case baseStack:
			parts[0].Current = stack.DigestCurrent
		case baseStack + "/" + stack.Feature:
			if stack.Present {
				parts = append(parts, provider.BootstrapPart{
					Name:    stack.Feature,
					Summary: b.summaryOf(stack.Feature),
					Current: stack.DigestCurrent,
					Shape:   featureShape(read, stack.Feature),
				})
			}
		default:
			parts = append(parts, provider.BootstrapPart{Name: stack.Name, Current: stack.DigestCurrent && stack.ReadError == ""})
		}
	}
	return parts
}

func featureShape(read survey, feature string) *provider.PartShape {
	if feature != albFeature && feature != albShieldedFeature {
		return nil
	}
	shape, err := alb.Shape(pricing.EdgeSite{Tier: read.Tier, Region: read.Region})
	if err != nil {
		return nil
	}
	return &provider.PartShape{Vendor: shape.Vendor, Region: shape.Region, Resources: shape.Shared}
}
