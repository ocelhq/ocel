package provider

import (
	"context"

	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func (p *Provider) ContainerArch(_ context.Context, app, declared string) (string, error) {
	return deploy.ContainerArch(app, declared)
}

func (p *Provider) ContainerRuntime(_ context.Context, arch string) ([]byte, error) {
	held, err := payloads.ContainerRuntime(arch)
	if err != nil {
		return nil, err
	}
	return held.Bytes, nil
}
