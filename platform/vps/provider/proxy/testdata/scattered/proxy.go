package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Scattered)(nil)

func (s *Scattered) Guarantees() proxy.Guarantees { return s.guarantees }

func (s *Scattered) Admit(ctx context.Context, admission proxy.Admission) error {
	return s.admit(ctx, admission)
}

func (s *Scattered) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (s *Scattered) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
