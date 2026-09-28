package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	costVendor           = "aws"
	tfDistribution       = "aws_cloudfront_distribution"
	priceClass           = "PriceClass_All"
	previewWildcardShape = "preview-wildcard"
)

func Shape(site pricing.EdgeSite) (pricing.EdgeShape, error) {
	shape := pricing.EdgeShape{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Environment: []pricing.Shaped{distribution(site.Slug)},
	}
	if site.Class == edge.ClassPreview {
		shape.Shared = []pricing.Shaped{distribution(previewWildcardShape)}
	}
	return shape, nil
}

func distribution(name string) pricing.Shaped {
	return pricing.Shaped{Name: name, Type: tfDistribution, Properties: map[string]any{"price_class": priceClass}}
}
