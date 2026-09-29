package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/router"
)

type liveness struct{ *Provider }

func (p liveness) ServingRouter(_ context.Context, hostname string) (router.Kind, error) {
	if err := p.probes.next(hostname); err != nil {
		return "", err
	}
	return p.edges.answering(hostname), nil
}

func (p liveness) LastProbeFailure(hostname string) string { return p.probes.lastFor(hostname) }
