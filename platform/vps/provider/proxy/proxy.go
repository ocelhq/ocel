package proxy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

type Proxy interface {
	Guarantees() Guarantees
	Render(spec Spec) ([]byte, error)
	File() string
	Unrendered(config []byte, permission Permission) string
	Reload(ctx context.Context) error
	Inspect(ctx context.Context) (Standing, error)
	Certificate(ctx context.Context, hostname string) (Certificate, error)
}

const (
	BuiltinContainer = "ocel-proxy"
	HTTPPort         = "80"
	HTTPSPort        = "443"
)

type Guarantees struct {
	OwnsPorts bool
}

type Spec struct {
	Pins        []Pin
	Hostnames   []string
	PreviewBase string
	Upstream    string
	Edge        string
	Permission  Permission
}

type Permission struct {
	Dial string
	Path string
}

type Pin struct {
	Hostname string
	Path     string
}

type Standing []provider.HostCheck

type Certificate struct {
	Renewal string
	Trouble error
}
