package bootstrap

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	BastionPart    = "bastion"
	baseSummary    = "the database, buckets, keys, secrets, accounts and roles every deploy in this tier runs on"
	BastionSummary = "the service a port forward runs inside the tier's network"
)

func DescribeParts(ctx context.Context, b Engine, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	read, err := b.surveyFor(ctx, described.Tier)
	if err != nil {
		return nil, err
	}
	if !read.Present {
		return nil, nil
	}
	parts := SurveyedParts(b, read, described.Stacks)
	added, err := b.ListDescribedParts(ctx, read.Tier)
	if err != nil {
		return nil, err
	}
	parts = append(parts, added...)
	made, err := b.describeMadeParts(ctx, read.Tier)
	if err != nil {
		return nil, err
	}
	return append(parts, made...), nil
}

func SurveyedParts(b Engine, read Survey, stacks []provider.BootstrapStack) []provider.BootstrapPart {
	baseStack := read.Project + "/" + string(read.Tier)
	base := provider.BootstrapPart{
		Name:    provider.BasePart,
		Summary: baseSummary,
		Digest:  read.Stamp.Digest,
		Shape:   &provider.PartShape{Vendor: string(gcloud.Vendor), Region: read.Region},
	}
	for _, item := range Resources(read.Names, read.Tier, read.Emulated) {
		base.Resources = append(base.Resources, provider.PartResource{Kind: string(item.Kind), ID: item.Name})
		if shaped, priced := item.Shape(read.Region); priced {
			base.Shape.Resources = append(base.Shape.Resources, shaped)
		}
	}
	parts := []provider.BootstrapPart{base}
	for _, stack := range stacks {
		switch stack.Name {
		case baseStack:
			parts[0].Current = stack.DigestCurrent
		case baseStack + "/" + stack.Part:
			if stack.Present {
				parts = append(parts, provider.BootstrapPart{
					Name:    stack.Part,
					Summary: b.summaryOf(stack.Part),
					Current: stack.DigestCurrent,
					Shape:   partShape(read, stack.Part),
				})
			}
		default:
			parts = append(parts, provider.BootstrapPart{Name: stack.Name, Current: stack.DigestCurrent && stack.ReadError == ""})
		}
	}
	return parts
}

func partShape(read Survey, part string) *provider.PartShape {
	if part != CloudflareOriginPart {
		return nil
	}
	shape, err := alb.Shape(pricing.EdgeSite{Tier: read.Tier, Region: read.Region})
	if err != nil {
		return nil
	}
	return &provider.PartShape{Vendor: shape.Vendor, Region: shape.Region, Resources: shape.Shared}
}

func (b Engine) describeMadeParts(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error) {
	var made []provider.BootstrapPart
	for _, each := range b.Removable {
		if each.Describe == nil {
			continue
		}
		described, present, err := each.Describe(ctx, b, tier)
		if err != nil {
			return nil, err
		}
		if present {
			made = append(made, described)
		}
	}
	return made, nil
}
