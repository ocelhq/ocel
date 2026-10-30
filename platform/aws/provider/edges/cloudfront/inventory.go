package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	costVendor          = "aws"
	tfDistribution      = "aws_cloudfront_distribution"
	priceClass          = "PriceClass_All"
	previewWildcardItem = "preview-wildcard"
)

func Inventory(site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	inventory := pricing.EdgeInventory{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Environment: []pricing.Resource{distribution(site.Slug)},
	}
	if site.Tier == environment.TierPreview {
		inventory.Shared = []pricing.Resource{distribution(previewWildcardItem)}
	}
	return inventory, nil
}

func distribution(name string) pricing.Resource {
	return pricing.Resource{Name: name, Type: tfDistribution, Properties: map[string]any{"price_class": priceClass}}
}
