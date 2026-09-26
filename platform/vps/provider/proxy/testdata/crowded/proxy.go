package crowded

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Crowded)(nil)

func (c *Crowded) Guarantees() proxy.Guarantees { return c.guarantees }

func (c *Crowded) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (c *Crowded) File() string { return "" }

func (c *Crowded) Unrendered([]byte, proxy.Permission) string { return "" }

func (c *Crowded) Reload(context.Context) error { return nil }

func (c *Crowded) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (c *Crowded) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}


const upstream = "localhost:8080"

func (c *Crowded) admit(_ context.Context, spec proxy.Spec) error {
	if spec.Upstream == "" {
		spec.Upstream = upstream
	}
	c.admitted = spec
	return nil
}
