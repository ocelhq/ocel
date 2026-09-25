package vps

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type containerRuntime struct{ *Provider }

func (p containerRuntime) Arch(ctx context.Context, app, declared string) (string, error) {
	runs, err := p.host.Arch(ctx)
	if err != nil {
		return "", err
	}
	return host.ContainerArch(app, declared, runs)
}

func (p containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	return host.ContainerRuntime(arch)
}
