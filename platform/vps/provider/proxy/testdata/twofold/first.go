package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type First struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}

func (f *First) admit(_ context.Context, admission proxy.Admission) error {
	f.admitted = admission
	return nil
}
