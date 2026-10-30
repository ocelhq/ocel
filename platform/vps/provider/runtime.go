package vps

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"

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
	return host.ContainerRuntime(p.assets, arch)
}

func (p containerRuntime) FunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	goarch, err := readFunctionArch(framework)
	if err != nil {
		return nil, err
	}
	return images.PullNodeBase(ctx, p.readImagePull(), goarch)
}

func (p containerRuntime) FunctionFiles(_ context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	goarch, err := readFunctionArch(framework)
	if err != nil {
		return nil, err
	}
	return host.NextContainerRuntime(p.assets, goarch)
}

func (p containerRuntime) FunctionBuildPaths(buildoutput.Framework) []string {
	return []string{edge.StaticAssetDir, naming.ImageConfigFile}
}

func readFunctionArch(framework buildoutput.Framework) (string, error) {
	if err := functioncode.RefuseContainerFramework(framework); err != nil {
		return "", err
	}
	goarch, known := arch.GoArch(framework.Arch)
	if !known {
		return "", refusal.Refuse(refusal.CodeInvalid, "this function is built for %s: build it for %s or %s", framework.Arch, arch.X8664, arch.ARM64)
	}
	return goarch, nil
}

func (p containerRuntime) readImagePull() images.ImagePull {
	if p.pullImage != nil {
		return p.pullImage
	}
	return images.PullRemoteImage
}
