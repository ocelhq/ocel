package late

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type Late struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (l *Late) admit(_ context.Context, spec proxy.Spec) error {
	l.admitted = spec
	return nil
}
