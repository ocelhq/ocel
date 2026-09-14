package apigateway

import (
	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	costVendor   = "aws"
	tfRestAPI    = "aws_api_gateway_rest_api"
	endpointType = "REGIONAL"
)

var _ costkit.EdgeInventorier = (*provider)(nil)

func (p *provider) CostInventory(site costkit.EdgeSite) (costkit.EdgeInventory, error) {
	return costkit.EdgeInventory{
		Vendor: costVendor,
		Region: site.Region,
		Environment: []costkit.Item{{
			Name: site.Slug, Type: tfRestAPI,
			Properties: map[string]any{"endpoint_configuration": map[string]any{"types": []any{endpointType}}},
		}},
	}, nil
}
