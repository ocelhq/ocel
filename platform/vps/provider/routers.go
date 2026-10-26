package vps

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/edges/switchboard"
)

type routers struct{ provider *Provider }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	switch kind {
	case boxapi.SwitchboardRouter:
		return switchboard.NewRouter(r.provider.box()), nil
	case router.Kind(cloudflare.Kind):
		return cloudflare.NewRouter(), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no %q", kind)
}
