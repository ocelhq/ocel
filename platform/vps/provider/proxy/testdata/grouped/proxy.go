package grouped

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var (
	_ proxy.Proxy  = (*Grouped)(nil)
	_ fmt.Stringer = (*Grouped)(nil)
)

func (*Grouped) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (*Grouped) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (*Grouped) Unrendered([]byte, proxy.Admission) string { return "" }

func (*Grouped) Reload(context.Context) error { return nil }

func (*Grouped) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (*Grouped) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (*Grouped) Forget(context.Context, []string) ([]string, error) { return nil, nil }
