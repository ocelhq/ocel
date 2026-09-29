package caddyfile

import (
	"context"
	"path/filepath"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Caddyfile{}

func (Caddyfile) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (c Caddyfile) Render(spec proxy.Spec) ([]byte, error) { return c.render(spec.Hostnames), nil }

func (c Caddyfile) File() string { return filepath.Join(c.Directory, FileName) }

func (Caddyfile) Unrendered([]byte, proxy.Permission) string { return "" }

func (c Caddyfile) Unrouted(ctx context.Context, hostnames []string) error {
	return c.unrouted(ctx, hostnames)
}

func (c Caddyfile) Validate(ctx context.Context, rendered []byte) error {
	return c.validate(ctx, rendered)
}

func (c Caddyfile) Reload(ctx context.Context) error { return c.reload(ctx) }

func (c Caddyfile) Inspect(ctx context.Context) (proxy.Checks, error) { return c.inspect(ctx) }

func (Caddyfile) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{Renewal: Renewal}, nil
}
