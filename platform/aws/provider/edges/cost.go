package edges

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
)

var shapes = map[edge.Kind]func(account.Namespace, pricing.EdgeSite) (pricing.EdgeShape, error){
	cloudflare.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return cloudflare.Shape(site)
	},
	cloudfront.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return cloudfront.Shape(site)
	},
	apigateway.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
		return apigateway.Shape(site)
	},
}

func Shape(kind edge.Kind, ns account.Namespace, site pricing.EdgeSite) (pricing.EdgeShape, error) {
	shape, shaped := shapes[kind]
	if !shaped {
		return pricing.EdgeShape{}, nil
	}
	return shape(ns, site)
}

var Rates = []pricing.EdgeRates{cloudflarecost.Rates}
