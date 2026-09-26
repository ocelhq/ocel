package fake

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type liveness struct{ *Provider }

func (p liveness) ServingEdge(_ context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	return p.edges.answering(hostname), nil
}

func (liveness) LastProbeFailure(string) string { return "" }
