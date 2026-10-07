package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
	forwards := make([]provider.PortForward, 0, len(req.Bindings))
	for _, binding := range req.Bindings {
		local, err := p.host.ForwardToContainer(ctx, binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort])
		if err != nil {
			return nil, err
		}
		forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: local})
	}
	return forwards, nil
}
