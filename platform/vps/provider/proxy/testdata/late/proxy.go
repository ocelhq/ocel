package late

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (l *Late) Guarantees() proxy.Guarantees { return l.guarantees }

var _ proxy.Proxy = (*Late)(nil)

func (l *Late) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (l *Late) Unrendered([]byte, proxy.Permission) string { return "" }

func (l *Late) Reload(context.Context) error { return nil }

func (l *Late) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (l *Late) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

