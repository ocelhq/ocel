package incomplete

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Incomplete)(nil)

func (i *Incomplete) Guarantees() proxy.Guarantees { return i.guarantees }

func (i *Incomplete) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (i *Incomplete) Unrendered([]byte, proxy.Permission) string { return "" }

func (i *Incomplete) Reload(context.Context) error { return nil }

func (i *Incomplete) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }
