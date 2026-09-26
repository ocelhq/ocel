package dotted

import (
	"context"

	. "github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ Proxy = Dotted{}

func (Dotted) Guarantees() Guarantees { return Guarantees{} }

func (Dotted) Render(Spec) ([]byte, error) { return nil, nil }

func (Dotted) File() string { return "" }

func (Dotted) Unrendered([]byte, proxy.Permission) string { return "" }

func (Dotted) Reload(context.Context) error { return nil }

func (Dotted) Inspect(context.Context) (Standing, error) { return nil, nil }

func (Dotted) Certificate(context.Context, string) (Certificate, error) {
	return Certificate{}, nil
}

