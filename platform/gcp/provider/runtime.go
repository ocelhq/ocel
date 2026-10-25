package gcp

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

type containerRuntime struct{ *Provider }

func (p containerRuntime) Arch(_ context.Context, app, declared string) (string, error) {
	if runs, _ := arch.GoArch(declared); runs != payloads.ContainerArch {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares arch %q, and Cloud Run runs %s alone: drop the arch, or deploy %s to a provider that runs %s",
			app, declared, arch.X8664, app, declared)
	}
	return payloads.ContainerArch, nil
}

func (p containerRuntime) Binary(_ context.Context, architecture string) ([]byte, error) {
	return payloads.ContainerRuntime(architecture)
}

func (p containerRuntime) FunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	if err := functioncode.RefuseContainerFramework(framework); err != nil {
		return nil, err
	}
	return p.functionImages().ResolveBase(ctx, framework)
}

func (p containerRuntime) FunctionFiles(ctx context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	if err := functioncode.RefuseContainerFramework(framework); err != nil {
		return nil, err
	}
	return p.functionImages().ReadRuntime(ctx, framework)
}

func (p containerRuntime) FunctionBuildPaths(buildoutput.Framework) []string { return nil }
