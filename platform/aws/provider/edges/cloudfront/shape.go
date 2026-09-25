package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	costVendor           = "aws"
	tfDistribution       = "aws_cloudfront_distribution"
	priceClass           = "PriceClass_All"
	previewWildcardShape = "preview-wildcard"
)

var _ costkit.EdgeCost = (*provider)(nil)

func (p *provider) Shape(site costkit.EdgeSite) (costkit.EdgeShape, error) {
	shape := costkit.EdgeShape{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Environment: []costkit.Shaped{distribution(site.Slug)},
	}
	if site.Class == edge.ClassPreview {
		shape.Shared = []costkit.Shaped{distribution(previewWildcardShape)}
	}
	return shape, nil
}

func distribution(name string) costkit.Shaped {
	return costkit.Shaped{Name: name, Type: tfDistribution, Properties: map[string]any{"price_class": priceClass}}
}
