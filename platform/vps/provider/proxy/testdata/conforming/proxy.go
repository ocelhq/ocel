package conforming

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Conforming)(nil)

func (c *Conforming) Guarantees() proxy.Guarantees { return c.guarantees }

func (c *Conforming) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (c *Conforming) File() string { return "" }

func (c *Conforming) Unrendered([]byte, proxy.Permission) string { return "" }

func (c *Conforming) RefuseRouted(context.Context, []string) error { return nil }

func (c *Conforming) Validate(context.Context, []byte) error { return nil }

func (c *Conforming) Reload(context.Context, proxy.Spec) error { return nil }

func (c *Conforming) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (c *Conforming) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (c *Conforming) RefuseUnshielded(context.Context, string) error { return nil }
