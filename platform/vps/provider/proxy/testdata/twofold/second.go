package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Second{}

type Second struct{}

func (Second) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Second) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Second) File() string { return "" }

func (Second) Unrendered([]byte, proxy.Permission) string { return "" }

func (Second) RefuseRouted(context.Context, []string) error { return nil }

func (Second) Validate(context.Context, []byte) error { return nil }

func (Second) Reload(context.Context, proxy.Spec) error { return nil }

func (Second) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (Second) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (Second) RefuseUnshielded(context.Context, string) error { return nil }
