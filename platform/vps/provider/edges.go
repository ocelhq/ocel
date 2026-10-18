package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/edges/switchboard"
)

type edges struct{ provider *Provider }

var supportedEdges = []edge.Kind{cloudflare.Kind}

func (e edges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.provider.box(), nil
	case cloudflare.Kind:
		decoded, err := cloudflare.DecodeOptions(options)
		if err != nil {
			return nil, err
		}
		namespace, err := provider.NamespaceFromEnv()
		if err != nil {
			return nil, err
		}
		return boxProxy{Proxy: cloudflare.NewProxy(namespace.String(), decoded), box: e.provider.box()}, nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"edge %q is not supported: leave `provider.vps.edge` out, and the proxy on the box answers the project's hostnames, or name %s to front it", kind, cloudflare.Kind)
}

type boxProxy struct {
	*cloudflare.Proxy
	box *switchboard.Edge
}

func (x boxProxy) Hooks() edge.Hooks {
	hooks := x.Proxy.Hooks()
	hooks.PublishGate = x.box.PublishGateBehind(hooks.PurgeHostnames)
	return hooks
}

func (x boxProxy) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	if err := x.box.ReconcileGateBehind(ctx, spec, prior, x.Proxy.Hooks().PurgeHostnames); err != nil {
		return nil, err
	}
	return x.Proxy.Reconcile(ctx, spec, prior)
}

func (x boxProxy) Facts() edge.Facts {
	facts := x.Proxy.Facts()
	facts.Gates = true
	return facts
}

func (p *Provider) box() *switchboard.Edge {
	return switchboard.New(p.host, p.routing, p.host.Tunnel(), p.applyOrigins, p.options.SSH.session().Destination(), p.findTunnelHooks, newRecordedGate(p.keyValues, p.cipher))
}

func (p *Provider) findTunnelHooks(kind edge.Kind) (*edge.TunnelHooks, error) {
	front, err := edges{provider: p}.Open(kind, nil)
	if err != nil {
		return nil, err
	}
	if tunnels := front.Hooks().Tunnels; tunnels != nil {
		return tunnels, nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"the %s edge opens no tunnel and names no connector for this box to run\nRemove `tunnel` from the %s edge's options", kind, kind)
}
