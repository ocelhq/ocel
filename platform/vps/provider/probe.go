package vps

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Serving(ctx context.Context, kind edge.Kind, hostname string) (edge.Kind, error) {
	if edge.Loopback(hostname) {
		return p.servedOnTheBox(ctx, hostname)
	}
	return p.liveness.Serving(ctx, kind, hostname)
}

func (p *Provider) servedOnTheBox(ctx context.Context, hostname string) (edge.Kind, error) {
	said, err := p.host.ServedEdge(ctx, edge.ProbeHostname(hostname))
	if err != nil {
		return "", err
	}
	p.stopped(hostname, said.Unreached)
	return edge.Kind(said.Edge), nil
}

func (p *Provider) Unreached(hostname string) string {
	if !edge.Loopback(hostname) {
		return p.liveness.Unreached(hostname)
	}
	p.probed.Lock()
	defer p.probed.Unlock()
	return p.unreached[hostname]
}

func (p *Provider) stopped(hostname, cause string) {
	p.probed.Lock()
	defer p.probed.Unlock()
	if cause == "" {
		delete(p.unreached, hostname)
		return
	}
	if p.unreached == nil {
		p.unreached = map[string]string{}
	}
	p.unreached[hostname] = cause
}
