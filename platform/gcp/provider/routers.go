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
		"this provider cannot front deployments with the %q edge; it fronts them with %q or %q", kind, direct.Kind, alb.Kind)
}
