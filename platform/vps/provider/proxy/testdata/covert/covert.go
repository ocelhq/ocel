package covert

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Covert struct{}

func (Covert) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Covert) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Covert) File() string { return "" }

func (Covert) Unrendered([]byte, proxy.Permission) string { return "" }

func (Covert) RefuseRouted(context.Context, []string) error { return nil }

func (Covert) Validate(context.Context, []byte) error { return nil }

func (Covert) Reload(context.Context, proxy.Spec) error { return nil }

func (Covert) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (Covert) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (Covert) RefuseUnshielded(context.Context, string) error { return nil }
