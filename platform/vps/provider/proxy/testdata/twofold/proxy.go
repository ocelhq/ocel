package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*First)(nil)

func (f *First) Guarantees() proxy.Guarantees { return f.guarantees }

func (f *First) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (f *First) Unrendered([]byte, proxy.Permission) string { return "" }

func (f *First) Reload(context.Context) error { return nil }

func (f *First) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (f *First) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

