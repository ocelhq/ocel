package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
	forwards := make([]provider.PortForward, 0, len(req.Bindings))
	for _, binding := range req.Bindings {
		local, stop, err := p.host.ForwardToContainer(ctx, binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort])
		if err != nil {
			for _, opened := range forwards {
				opened.Close()
			}
			return nil, err
		}
		forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: local, Close: stop})
	}
	return forwards, nil
}
