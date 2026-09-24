package late

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Late struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (l *Late) admit(_ context.Context, admission proxy.Admission) error {
	l.admitted = admission
	return nil
}
