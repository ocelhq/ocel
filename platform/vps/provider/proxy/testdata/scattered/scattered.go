package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Scattered struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (s *Scattered) admit(_ context.Context, spec proxy.Spec) error {
	s.admitted = spec
	return nil
}
