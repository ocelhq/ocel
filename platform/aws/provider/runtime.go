package aws

import (
	"context"

	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type containerRuntime struct{ *Provider }

func (p containerRuntime) Arch(_ context.Context, app, declared string) (string, error) {
	return deploy.ContainerArch(app, declared)
}

func (p containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	payload, err := payloads.ContainerRuntime(arch)
	if err != nil {
		return nil, err
	}
	return payload.Bytes, nil
}
