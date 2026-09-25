package misnamed

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Misnamed)(nil)

func (m *Misnamed) Guarantees() proxy.Guarantees { return m.guarantees }

func (m *Misnamed) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (m *Misnamed) Unrendered([]byte, proxy.Permission) string { return "" }

func (m *Misnamed) Reload(context.Context) error { return nil }

func (m *Misnamed) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (m *Misnamed) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (m *Misnamed) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
