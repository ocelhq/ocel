package conforming

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Conforming)(nil)

func (c *Conforming) Guarantees() proxy.Guarantees { return c.guarantees }

func (c *Conforming) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (c *Conforming) Unrendered([]byte, proxy.Permission) string { return "" }

func (c *Conforming) Reload(context.Context) error { return nil }

func (c *Conforming) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (c *Conforming) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (c *Conforming) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
