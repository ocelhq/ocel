package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	costVendor          = "aws"
	tfDistribution      = "aws_cloudfront_distribution"
	priceClass          = "PriceClass_All"
	previewWildcardItem = "preview-wildcard"
)

var _ costkit.EdgeInventorier = (*provider)(nil)

func (p *provider) CostInventory(site costkit.EdgeSite) (costkit.EdgeInventory, error) {
	inventory := costkit.EdgeInventory{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Environment: []costkit.Item{distribution(site.Slug)},
	}
	if site.Class == edge.ClassPreview {
		inventory.Shared = []costkit.Item{distribution(previewWildcardItem)}
	}
	return inventory, nil
}

func distribution(name string) costkit.Item {
	return costkit.Item{Name: name, Type: tfDistribution, Properties: map[string]any{"price_class": priceClass}}
}
