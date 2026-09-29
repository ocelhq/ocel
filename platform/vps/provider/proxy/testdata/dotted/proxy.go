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

func (Dotted) RefuseRouted(context.Context, []string) error { return nil }

func (Dotted) Validate(context.Context, []byte) error { return nil }

func (Dotted) Reload(context.Context, Spec) error { return nil }

func (Dotted) Inspect(context.Context) (Checks, error) { return nil, nil }

func (Dotted) Certificate(context.Context, string) (Certificate, error) {
	return Certificate{}, nil
}

func (Dotted) RefuseUnshielded(context.Context, string) error { return nil }

func (Dotted) OriginFiles(Spec) ([]OriginFile, error) { return nil, nil }
