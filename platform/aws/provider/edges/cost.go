package edges

import (
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var shapes = map[edge.Kind]func(bootstrap.Namespace, pricing.EdgeSite) (pricing.EdgeShape, error){
	cloudflare.Kind: func(ns bootstrap.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return cloudflare.Shape(string(ns), site)
	},
	cloudfront.Kind: func(_ bootstrap.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return cloudfront.Shape(site)
	},
	apigateway.Kind: func(_ bootstrap.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return apigateway.Shape(site)
	},
}

func Shape(kind edge.Kind, ns bootstrap.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
	shape, shaped := shapes[kind]
	if !shaped {
		return pricing.EdgeShape{}, nil
	}
	return shape(ns, site)
}

var Rates = []pricing.EdgeRates{cloudflarecost.Rates}
