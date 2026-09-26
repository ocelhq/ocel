package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Liveness interface {
	ServingEdge(ctx context.Context, kind edge.Kind, hostname string) (edge.Kind, error)

	LastProbeFailure(hostname string) string
}
