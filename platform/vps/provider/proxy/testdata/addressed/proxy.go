package addressed

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = &Addressed{}

func (*Addressed) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (*Addressed) Admit(context.Context, proxy.Admission) error { return nil }

func (*Addressed) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (*Addressed) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (*Addressed) Forget(context.Context, []string) ([]string, error) { return nil, nil }
