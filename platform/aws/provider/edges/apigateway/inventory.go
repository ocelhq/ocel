package apigateway

import (
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	costVendor   = "aws"
	tfRestAPI    = "aws_api_gateway_rest_api"
	endpointType = "REGIONAL"
)

func Inventory(site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	return pricing.EdgeInventory{
		Vendor: costVendor,
		Region: site.Region,
		Environment: []pricing.Resource{{
			Name: site.Slug, Type: tfRestAPI,
			Properties: map[string]any{"endpoint_configuration": map[string]any{"types": []any{endpointType}}},
		}},
	}, nil
}
