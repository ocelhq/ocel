package unasserted

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (u *Unasserted) Guarantees() proxy.Guarantees { return u.guarantees }

func (u *Unasserted) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (u *Unasserted) File() string { return "" }

func (u *Unasserted) Unrendered([]byte, proxy.Permission) string { return "" }

func (u *Unasserted) Reload(context.Context) error { return nil }

func (u *Unasserted) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (u *Unasserted) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

