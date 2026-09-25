package twofold

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type First struct {
	guarantees proxy.Guarantees
	admitted   proxy.Spec
}

func (f *First) admit(_ context.Context, spec proxy.Spec) error {
	f.admitted = spec
	return nil
}
