package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/router"
)

type liveness struct{ *Provider }

func (p liveness) ServingRouter(_ context.Context, hostname string) (router.Kind, error) {
	return p.edges.answering(hostname), nil
}

func (liveness) LastProbeFailure(string) string { return "" }
