package proxy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

type Proxy interface {
	Guarantees() Guarantees
	Render(spec Spec) ([]byte, error)
	File() string
	Unrendered(config []byte, permission Permission) string
	RefuseRouted(ctx context.Context, hostnames []string) error
	Validate(ctx context.Context, rendered []byte) error
	Reload(ctx context.Context, served Spec) error
	Inspect(ctx context.Context) (Checks, error)
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
	Pins               []Pin
	ClientCertificates []ClientCertificate
	Hostnames          []string
	PreviewBase        string
	Upstream           string
	Router             router.Kind
	Permission         Permission
}

type Permission struct {
	Dial string
	Path string
}

type ClientCertificate struct {
	Hostname    string
	Certificate string
}

type Pin struct {
	Hostname string
	Path     string
}

type Checks []provider.HostCheck

type Certificate struct {
	Renewal string
	Trouble error
}
