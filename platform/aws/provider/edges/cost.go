package edges

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var shapes = map[edge.Kind]func(bootstrap.Namespace, costkit.EdgeSite) (costkit.EdgeShape, error){
	cloudflare.Kind: func(ns bootstrap.Namespace, site costkit.EdgeSite) (costkit.EdgeShape, error) {
		return cloudflare.Shape(string(ns), site)
	},
	cloudfront.Kind: func(_ bootstrap.Namespace, site costkit.EdgeSite) (costkit.EdgeShape, error) {
		return cloudfront.Shape(site)
	},
	apigateway.Kind: func(_ bootstrap.Namespace, site costkit.EdgeSite) (costkit.EdgeShape, error) {
		return apigateway.Shape(site)
	},
}

func Shape(kind edge.Kind, ns bootstrap.Namespace, site costkit.EdgeSite) (costkit.EdgeShape, error) {
	shape, shaped := shapes[kind]
	if !shaped {
		return costkit.EdgeShape{}, nil
	}
	return shape(ns, site)
}

var Rates = []costkit.EdgeRates{cloudflarecost.Rates}
