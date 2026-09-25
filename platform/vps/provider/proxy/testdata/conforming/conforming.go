package conforming

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Conforming struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (c *Conforming) admit(_ context.Context, spec proxy.Spec) error {
	c.admitted = spec
	return nil
}
