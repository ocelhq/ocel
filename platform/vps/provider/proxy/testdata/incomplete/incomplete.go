package incomplete

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Incomplete struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (i *Incomplete) admit(_ context.Context, admission proxy.Admission) error {
	i.admitted = admission
	return nil
}
