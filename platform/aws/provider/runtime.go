package aws

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
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

func (p containerRuntime) FunctionBase(_ context.Context, framework buildoutput.Framework) (v1.Image, error) {
	return nil, refusal.Refuse(refusal.CodeNotReady, "this provider ships no base a container built from a %s function runs on", framework.Name)
}

func (p containerRuntime) FunctionFiles(_ context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	return nil, refusal.Refuse(refusal.CodeNotReady, "this provider ships no runtime a container built from a %s function boots through", framework.Name)
}
