package constrained

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Constrained{}

func (Constrained) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Constrained) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (Constrained) Unrendered([]byte) string { return "" }

func (Constrained) Reload(context.Context) error { return nil }

func (Constrained) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (Constrained) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (Constrained) Forget(context.Context, []string) ([]string, error) { return nil, nil }
