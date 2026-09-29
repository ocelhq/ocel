package traefik

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Traefik{}

func (Traefik) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (t Traefik) Render(spec proxy.Spec) ([]byte, error) { return t.render(spec) }

func (t Traefik) File() string { return t.file() }

func (Traefik) Unrendered([]byte, proxy.Permission) string { return "" }

func (t Traefik) RefuseRouted(ctx context.Context, hostnames []string) error {
	return t.refuseRouted(ctx, hostnames)
}

func (t Traefik) Validate(_ context.Context, rendered []byte) error { return t.validate(rendered) }

func (t Traefik) Reload(ctx context.Context) error { return t.reload(ctx) }

func (t Traefik) Inspect(ctx context.Context) (proxy.Checks, error) { return t.inspect(ctx) }

func (t Traefik) Certificate(ctx context.Context, hostname string) (proxy.Certificate, error) {
	spec, err := t.Box.ReadSpec(ctx)
	if err != nil {
		return proxy.Certificate{}, err
	}
	return t.certificate(ctx, spec, hostname)
}
