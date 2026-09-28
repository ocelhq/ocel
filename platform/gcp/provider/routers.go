package gcp

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type routers struct{ edges edges }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	switch kind {
	case router.Kind(direct.Kind):
		return direct.NewRouter(r.edges.openDirect()), nil
	case router.Kind(alb.Kind):
		return alb.NewRouter(r.edges.openALB()), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"this provider routes through %q and %q, not %q", direct.Kind, alb.Kind, kind)
}
