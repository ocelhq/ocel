package unasserted

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (u *Unasserted) Guarantees() proxy.Guarantees { return u.guarantees }

func (u *Unasserted) Admit(ctx context.Context, admission proxy.Admission) error {
	return u.admit(ctx, admission)
}

func (u *Unasserted) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (u *Unasserted) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (u *Unasserted) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
