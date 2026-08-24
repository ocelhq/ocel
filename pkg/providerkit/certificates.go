package providerkit

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certifier interface {
	Certificate(ctx context.Context, kind edge.Kind, hostname string, report Reporter) (string, error)
}

func certificateFor(ctx context.Context, provider Provider, kind edge.Kind, hostname string, report Reporter) (string, error) {
	certifier, ok := provider.(Certifier)
	if !ok {
		return "", nil
	}
	return certifier.Certificate(ctx, kind, hostname, report)
}
