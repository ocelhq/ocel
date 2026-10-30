package control

import (
	"context"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

func EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req, cloudflarecost.Rates)
}
