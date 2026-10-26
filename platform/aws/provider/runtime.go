package aws

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/refusal"

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
	if err := refuseContainerFramework(framework); err != nil {
		return nil, err
	}
	goarch, err := container.Arch(framework.Name, framework.Arch)
	if err != nil {
		return nil, err
	}
	return images.PullNodeBase(ctx, goarch)
}

func (p containerRuntime) FunctionFiles(_ context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	if err := refuseContainerFramework(framework); err != nil {
		return nil, err
	}
	return payloads.NextContainerRuntime(), nil
}

func refuseContainerFramework(framework buildoutput.Framework) error {
	if framework.Name == buildoutput.FrameworkNext {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid, "a container is built from a Next function alone here, and this one is %s: use a Dockerfile", framework.Name)
}
