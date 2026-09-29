package unasserted

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (u *Unasserted) Guarantees() proxy.Guarantees { return u.guarantees }

func (u *Unasserted) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (u *Unasserted) File() string { return "" }

func (u *Unasserted) Unrendered([]byte, proxy.Permission) string { return "" }

func (u *Unasserted) Unrouted(context.Context, []string) error { return nil }

func (u *Unasserted) Validate(context.Context, []byte) error { return nil }

func (u *Unasserted) Reload(context.Context) error { return nil }

func (u *Unasserted) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (u *Unasserted) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
