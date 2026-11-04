package control

import (
	"context"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cost"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func refuseTakingInventory(context.Context, provider.InventoryRequest) (*costv1.ResourceSet, error) {
	return nil, provider.RefuseNeedsRunner("taking a deploy's inventory")
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req, edges.Rates...)
}
