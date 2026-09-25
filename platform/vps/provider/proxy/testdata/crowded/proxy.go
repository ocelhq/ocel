package crowded

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Crowded)(nil)

func (c *Crowded) Guarantees() proxy.Guarantees { return c.guarantees }

func (c *Crowded) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (c *Crowded) Unrendered([]byte) string { return "" }

func (c *Crowded) Reload(context.Context) error { return nil }

func (c *Crowded) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (c *Crowded) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (c *Crowded) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}

const upstream = "localhost:8080"

func (c *Crowded) admit(_ context.Context, admission proxy.Admission) error {
	if admission.Upstream == "" {
		admission.Upstream = upstream
	}
	c.admitted = admission
	return nil
}
