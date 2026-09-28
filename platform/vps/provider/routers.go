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
			"this provider routes through %q alone, not %q", box.Kind, kind)
	}
	return box.NewRouter(r.provider.box()), nil
}
