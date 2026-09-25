package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Second{}

type Second struct{}

func (Second) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Second) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Second) Unrendered([]byte, proxy.Permission) string { return "" }

func (Second) Reload(context.Context) error { return nil }

func (Second) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (Second) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

