package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/costkit"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const CostSource = "ocel"

type ShapeRequest struct {
	Plan       DeployPlan
	Edge       edge.Kind
	Features   []string
	Resources  []Resource
	Functions  map[string][]FunctionSpec
	Transforms []string
}

type Shaper interface {
	Shape(ctx context.Context, req ShapeRequest) (*costv1.ResourceSet, error)
}

type Pricer interface {
	Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)
}

func EdgePricers(registry EdgeRegistry) ([]costkit.EdgePricer, error) {
	var pricers []costkit.EdgePricer
	for _, kind := range registry.Supported() {
		front, err := registry.Open(kind)
		if err != nil {
			return nil, err
		}
		if pricer, prices := front.(costkit.EdgePricer); prices {
			pricers = append(pricers, pricer)
		}
	}
	return pricers, nil
}

func ProductionHostnames(app AppEntry) []string {
	for _, domains := range app.Manifest.GetDomains() {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			return domains.GetHostnames()
		}
	}
	return nil
}
