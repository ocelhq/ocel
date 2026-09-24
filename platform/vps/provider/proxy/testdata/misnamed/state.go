package misnamed

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Misnamed struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (m *Misnamed) admit(_ context.Context, admission proxy.Admission) error {
	m.admitted = admission
	return nil
}
