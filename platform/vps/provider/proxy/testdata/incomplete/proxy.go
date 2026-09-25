package incomplete

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Incomplete)(nil)

func (i *Incomplete) Guarantees() proxy.Guarantees { return i.guarantees }

func (i *Incomplete) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (i *Incomplete) Unrendered([]byte, proxy.Admission) string { return "" }

func (i *Incomplete) Reload(context.Context) error { return nil }

func (i *Incomplete) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (i *Incomplete) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
