package covert

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Overt)(nil)

func (o *Overt) Guarantees() proxy.Guarantees { return o.guarantees }

func (o *Overt) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (o *Overt) File() string { return "" }

func (o *Overt) Unrendered([]byte, proxy.Permission) string { return "" }

func (o *Overt) Reload(context.Context) error { return nil }

func (o *Overt) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (o *Overt) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

