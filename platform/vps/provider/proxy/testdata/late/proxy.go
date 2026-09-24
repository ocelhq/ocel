package late

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (l *Late) Guarantees() proxy.Guarantees { return l.guarantees }

var _ proxy.Proxy = (*Late)(nil)

func (l *Late) Admit(ctx context.Context, admission proxy.Admission) error {
	return l.admit(ctx, admission)
}

func (l *Late) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (l *Late) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (l *Late) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
