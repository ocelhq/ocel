package vps

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

type edges struct{ provider *Provider }

var supportedEdges = []edge.Kind{cloudflare.Kind}

func (e edges) Open(kind edge.Kind) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.provider.box(), nil
	case cloudflare.Kind:
		return cloudflare.NewProxy(e.provider.options.SSH.session().Destination()), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"edge %q is not supported: leave `edge` out, and the proxy on the box answers the project's hostnames, or name %s to front it", kind, cloudflare.Kind)
}

func (p *Provider) box() *box.Edge {
	return box.New(p.host, p.applyOrigins, p.options.SSH.session().Destination())
}
