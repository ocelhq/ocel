package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

type Liveness interface {
	ServingEdge(ctx context.Context, kind edge.Kind, hostname string) (edge.Kind, error)

	LastProbeFailure(hostname string) string
}
