package caddyfile

import (
	"context"

	"github.com/ocelhq/ocel/pkg/router"
)

const FileName = "ocel.caddy"

type Box interface {
	Ran(ctx context.Context, what string, argv []string) (string, error)
	Fed(ctx context.Context, what string, argv []string, stdin []byte) (string, error)
	Claimed(ctx context.Context) ([]string, error)
	Placed(ctx context.Context, path string) (string, error)
	Probe(ctx context.Context, hostname string) (answered router.Kind, failure string, err error)
}

type Caddyfile struct {
	Box       Box
	Preset    string
	Directory string
	Container string
	Config    string
	Network   string
	Port      int
}
