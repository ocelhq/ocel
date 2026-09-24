package unasserted

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Unasserted struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (u *Unasserted) admit(_ context.Context, admission proxy.Admission) error {
	u.admitted = admission
	return nil
}
