package caddyfile

import (
	"context"

	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const FileName = "ocel.caddy"

type Box interface {
	Ran(ctx context.Context, what string, argv []string) (string, error)
	RanWithStdin(ctx context.Context, what string, argv []string, stdin []byte) (string, error)
	ReadSpec(ctx context.Context) (proxy.Spec, error)
	PlacedSum(ctx context.Context, path string) (string, error)
	Probe(ctx context.Context, hostname string) (answered router.Kind, failure string, err error)
}

type Caddyfile struct {
	Box                Box
	Preset             string
	Directory          string
	ContainerDirectory string
	Container          string
	Config             string
	Network            string
	Port               int
}
