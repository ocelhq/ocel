package covert

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Covert struct{}

func (Covert) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Covert) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Covert) Unrendered([]byte, proxy.Permission) string { return "" }

func (Covert) Reload(context.Context) error { return nil }

func (Covert) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (Covert) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

