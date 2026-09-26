package constrained

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Constrained{}

func (Constrained) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Constrained) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Constrained) File() string { return "" }

func (Constrained) Unrendered([]byte, proxy.Permission) string { return "" }

func (Constrained) Reload(context.Context) error { return nil }

func (Constrained) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (Constrained) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
