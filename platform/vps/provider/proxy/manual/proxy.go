package manual

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Manual{}

func (Manual) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Manual) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (Manual) Unrendered([]byte, proxy.Admission) string { return "" }

func (Manual) Reload(context.Context) error { return nil }

func (m Manual) Inspect(ctx context.Context) (proxy.Standing, error) {
	standing := proxy.Standing{m.holding(ctx)}
	claimed, err := m.Box.Claimed(ctx)
	if err != nil {
		return nil, err
	}
	for _, hostname := range claimed {
		check, err := m.routing(ctx, hostname)
		if err != nil {
			return nil, err
		}
		standing = append(standing, check)
	}
	return standing, nil
}

func (Manual) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{Renewal: certs.AdoptedRenewal}, nil
}

func (Manual) Forget(context.Context, []string) ([]string, error) { return nil, nil }
