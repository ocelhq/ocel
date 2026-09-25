package misnamed

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Misnamed struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (m *Misnamed) admit(_ context.Context, spec proxy.Spec) error {
	m.admitted = spec
	return nil
}
