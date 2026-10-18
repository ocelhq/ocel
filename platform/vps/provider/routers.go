package vps

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/edges/switchboard"
)

type routers struct{ provider *Provider }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	if kind != boxapi.SwitchboardRouter {
		return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no %q", kind)
	}
	return switchboard.NewRouter(r.provider.box()), nil
}
