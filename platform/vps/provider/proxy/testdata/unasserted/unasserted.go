package unasserted

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Unasserted struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (u *Unasserted) admit(_ context.Context, spec proxy.Spec) error {
	u.admitted = spec
	return nil
}
