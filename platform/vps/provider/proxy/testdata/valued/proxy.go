package valued

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Valued{}

func (Valued) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Valued) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Valued) Unrendered([]byte, proxy.Permission) string { return "" }

func (Valued) Reload(context.Context) error { return nil }

func (Valued) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (Valued) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (Valued) Forget(context.Context, []string) ([]string, error) { return nil, nil }
