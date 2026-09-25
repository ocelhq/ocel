package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (s *Scattered) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}
