package incomplete

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Incomplete)(nil)

func (i *Incomplete) Guarantees() proxy.Guarantees { return i.guarantees }

func (i *Incomplete) Admit(ctx context.Context, admission proxy.Admission) error {
	return i.admit(ctx, admission)
}

func (i *Incomplete) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (i *Incomplete) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
