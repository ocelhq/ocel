package incomplete

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Incomplete struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (i *Incomplete) admit(_ context.Context, spec proxy.Spec) error {
	i.admitted = spec
	return nil
}
