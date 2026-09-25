package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Scattered)(nil)

func (s *Scattered) Guarantees() proxy.Guarantees { return s.guarantees }

func (s *Scattered) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (s *Scattered) Unrendered([]byte, proxy.Admission) string { return "" }

func (s *Scattered) Reload(context.Context) error { return nil }

func (s *Scattered) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (s *Scattered) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
