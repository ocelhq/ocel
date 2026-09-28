package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

type liveness struct{ *Provider }

func (p liveness) ServingEdge(_ context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	return p.edges.answering(hostname), nil
}

func (liveness) LastProbeFailure(string) string { return "" }
