package aws

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type containerRuntime struct{ *Provider }

func (p containerRuntime) Arch(_ context.Context, app, declared string) (string, error) {
	return container.Arch(app, declared)
}

func (p containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	payload, err := payloads.ContainerRuntime(arch)
	if err != nil {
		return nil, err
	}
	return payload.Bytes, nil
}

func (p containerRuntime) FunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	if err := functioncode.RefuseContainerFramework(framework); err != nil {
		return nil, err
	}
	goarch, err := container.Arch(framework.Name, framework.Arch)
	if err != nil {
		return nil, err
	}
	return images.PullNodeBase(ctx, p.readImagePull(), goarch)
}

func (p containerRuntime) FunctionFiles(_ context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	if err := functioncode.RefuseContainerFramework(framework); err != nil {
		return nil, err
	}
	return payloads.NextContainerRuntime(), nil
}

func (p containerRuntime) FunctionBuildPaths(buildoutput.Framework) []string { return nil }

func (p containerRuntime) readImagePull() images.ImagePull {
	if p.pullImage != nil {
		return p.pullImage
	}
	return images.PullRemoteImage
}
