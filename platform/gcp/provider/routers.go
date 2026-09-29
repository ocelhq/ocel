package gcp

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type routers struct{ edges edges }

func (r routers) Open(kind router.Kind) (router.Router, error) {
	switch kind {
	case cloudrun.RouterKind:
		return cloudrun.NewRouter(r.edges.openCloudRun()), nil
	case router.Kind(alb.Kind):
		return alb.NewRouter(r.edges.openALB()), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no %q", kind)
}
