package addressed

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = &Addressed{}

func (*Addressed) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (*Addressed) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (*Addressed) Unrendered([]byte, proxy.Permission) string { return "" }

func (*Addressed) Reload(context.Context) error { return nil }

func (*Addressed) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (*Addressed) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

