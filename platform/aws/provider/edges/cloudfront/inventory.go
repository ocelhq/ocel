package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	inventoryVendor     = "aws"
	tfDistribution      = "aws_cloudfront_distribution"
	priceClass          = "PriceClass_All"
	previewWildcardItem = "preview-wildcard"
)

func Inventory(site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	inventory := pricing.EdgeInventory{
		Vendor:      inventoryVendor,
		Region:      site.Region,
		BillsEgress: true,
		Environment: []pricing.Item{distribution(site.Slug)},
	}
	if site.Tier == environment.TierPreview {
		inventory.Shared = []pricing.Item{distribution(previewWildcardItem)}
	}
	return inventory, nil
}

func distribution(name string) pricing.Item {
	return pricing.Item{Name: name, Type: tfDistribution, Properties: map[string]any{"price_class": priceClass}}
}
