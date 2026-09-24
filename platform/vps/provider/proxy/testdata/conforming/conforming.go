package conforming

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Conforming struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (c *Conforming) admit(_ context.Context, admission proxy.Admission) error {
	c.admitted = admission
	return nil
}
