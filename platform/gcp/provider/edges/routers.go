package edges

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
)

type Routers struct{ Registry Registry }

func (r Routers) Open(kind router.Kind) (router.Router, error) {
	switch kind {
	case cloudrun.RouterKind:
		return cloudrun.NewRouter(r.Registry.OpenCloudRun()), nil
	case router.Kind(alb.Kind):
		return alb.NewRouter(r.Registry.OpenALB()), nil
	case router.Kind(cloudflare.Kind):
		return cloudflare.NewRouter(), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no %q", kind)
}
