package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*First)(nil)

func (f *First) Guarantees() proxy.Guarantees { return f.guarantees }

func (f *First) Admit(ctx context.Context, admission proxy.Admission) error {
	return f.admit(ctx, admission)
}

func (f *First) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (f *First) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (f *First) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
