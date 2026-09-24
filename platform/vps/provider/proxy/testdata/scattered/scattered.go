package scattered

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Scattered struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (s *Scattered) admit(_ context.Context, admission proxy.Admission) error {
	s.admitted = admission
	return nil
}
