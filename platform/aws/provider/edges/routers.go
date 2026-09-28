package edges

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var routerConstructors = map[router.Kind]func(Deps) router.Router{
	router.Kind(cloudflare.Kind): func(deps Deps) router.Router { return cloudflare.NewRouter(string(deps.Namespace)) },
	router.Kind(cloudfront.Kind): func(deps Deps) router.Router {
		return cloudfront.NewRouter(deps.Namespace, cloudfront.FromConfig(deps.AWS))
	},
	router.Kind(apigateway.Kind): func(deps Deps) router.Router {
		return apigateway.NewRouter(deps.Namespace, apigateway.FromConfig(deps.AWS))
	},
}

type Routers struct {
	Deps Deps
}

var _ provider.Routers = Routers{}

func (r Routers) Open(kind router.Kind) (router.Router, error) {
	construct, ok := routerConstructors[kind]
	if !ok {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"this provider routes through no %q; it routes through %s", kind, supportedList())
	}
	return construct(r.Deps), nil
}

func Pairings() []provider.Pairing {
	serverless := []provider.Compute{provider.ComputeServerless}
	return []provider.Pairing{
		{Edge: apigateway.Kind, Router: router.Kind(apigateway.Kind), Computes: serverless},
		{Edge: cloudflare.Kind, Router: router.Kind(cloudflare.Kind), Computes: serverless},
		{Edge: cloudfront.Kind, Router: router.Kind(cloudfront.Kind), Computes: provider.Computes()},
	}
}
