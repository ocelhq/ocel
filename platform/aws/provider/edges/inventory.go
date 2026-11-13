package edges

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var inventories = map[edge.Kind]func(account.Namespace, pricing.EdgeSite) (pricing.EdgeInventory, error){
	cloudflare.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeInventory, error) {
		return cloudflare.Inventory(site)
	},
	cloudfront.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeInventory, error) {
		return cloudfront.Inventory(site)
	},
	apigateway.Kind: func(_ account.Namespace, site pricing.EdgeSite) (pricing.EdgeInventory, error) {
		return apigateway.Inventory(site)
	},
}

func Inventory(kind edge.Kind, ns account.Namespace, site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	inventoryOf, fronted := inventories[kind]
	if !fronted {
		return pricing.EdgeInventory{}, nil
	}
	return inventoryOf(ns, site)
}
