package generic

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Generic[int])(nil)

func (*Generic[T]) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (*Generic[T]) Render(proxy.Admission) ([]byte, error) { return nil, nil }

func (*Generic[T]) Unrendered([]byte, proxy.Admission) string { return "" }

func (*Generic[T]) Reload(context.Context) error { return nil }

func (*Generic[T]) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (*Generic[T]) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func (*Generic[T]) Forget(context.Context, []string) ([]string, error) { return nil, nil }
