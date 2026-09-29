package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/router"
)

type Liveness interface {
	ServingRouter(ctx context.Context, hostname string) (router.Kind, error)

	LastProbeFailure(hostname string) string
}
