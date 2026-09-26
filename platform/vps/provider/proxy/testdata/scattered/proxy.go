package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Scattered)(nil)

func (s *Scattered) Guarantees() proxy.Guarantees { return s.guarantees }

func (s *Scattered) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (s *Scattered) File() string { return "" }

func (s *Scattered) Unrendered([]byte, proxy.Permission) string { return "" }

func (s *Scattered) Reload(context.Context) error { return nil }

func (s *Scattered) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }
