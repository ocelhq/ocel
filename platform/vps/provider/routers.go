package vps

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

type routers struct{ provider *Provider }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	if kind != router.Kind(box.Kind) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"edge %q is not supported; use %q", kind, box.Kind)
	}
	return box.NewRouter(r.provider.box()), nil
}
