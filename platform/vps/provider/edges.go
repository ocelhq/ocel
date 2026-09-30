package vps

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

type edges struct{ provider *Provider }

var supportedEdges = []edge.Kind{cloudflare.Kind}

func (e edges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.provider.box(), nil
	case cloudflare.Kind:
		decoded, err := provider.DecodeEdgeOptions[cloudflare.Options](cloudflare.Kind, options)
		if err != nil {
			return nil, err
		}
		namespace, err := provider.NamespaceFromEnv()
		if err != nil {
			return nil, err
		}
		return cloudflare.NewProxy(namespace.String(), decoded), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"edge %q is not supported: leave `edge` out, and the proxy on the box answers the project's hostnames, or name %s to front it", kind, cloudflare.Kind)
}

func (p *Provider) box() *box.Edge {
	return box.New(p.host, p.applyOrigins, p.options.SSH.session().Destination(), p.findTunnelHooks)
}

func (p *Provider) findTunnelHooks(kind edge.Kind) (*edge.TunnelHooks, error) {
	front, err := edges{provider: p}.Open(kind, nil)
	if err != nil {
		return nil, err
	}
	if tunnels := front.Hooks().Tunnels; tunnels != nil {
		return tunnels, nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid, "%s opens no tunnel to this box", kind)
}
