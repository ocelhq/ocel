//go:build ignore

package constrained

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Ignored{}

type Ignored struct{}

func (Ignored) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Ignored) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Ignored) File() string { return "" }

func (Ignored) Unrendered([]byte, proxy.Permission) string { return "" }

func (Ignored) Reload(context.Context) error { return nil }

func (Ignored) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (Ignored) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
