package apigateway

import (
	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	costVendor   = "aws"
	tfRestAPI    = "aws_api_gateway_rest_api"
	endpointType = "REGIONAL"
)

var _ costkit.EdgeShaper = (*provider)(nil)

func (p *provider) ShapeCost(site costkit.EdgeSite) (costkit.EdgeShape, error) {
	return costkit.EdgeShape{
		Vendor: costVendor,
		Region: site.Region,
		Environment: []costkit.Shaped{{
			Name: site.Slug, Type: tfRestAPI,
			Properties: map[string]any{"endpoint_configuration": map[string]any{"types": []any{endpointType}}},
		}},
	}, nil
}
