package vps

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type routers struct{ provider *Provider }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	if kind != switchboard.RouterKind {
		return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no %q", kind)
	}
	return box.NewRouter(r.provider.box()), nil
}
