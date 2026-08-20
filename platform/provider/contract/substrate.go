package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Substrate interface {
	Bootstrap(ctx context.Context, class edge.Class, features []string, progress Progress) (Bootstrapped, error)
	Describe(ctx context.Context, class edge.Class) (Bootstrapped, bool, error)
	PlanTeardown(ctx context.Context, class edge.Class) ([]edge.Surface, error)
	Teardown(ctx context.Context, class edge.Class, progress Progress) error
}

type Bootstrapped struct {
	Features []string
	Values   map[string]string
	Trust    edge.TrustBoundary
}
