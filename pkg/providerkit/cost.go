package providerkit

import (
	"context"

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
