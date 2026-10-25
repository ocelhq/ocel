package fake

import (
	"context"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
)

const RuntimeBinary = "the fake container runtime"

const FunctionEntrypoint = "export const fakeRuntime = true"

func (p *Provider) CarryingBuildPaths(paths ...string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buildPaths = paths
	return p
}

func (p *Provider) WrappingContainers(architecture string, binary []byte) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runtimeArch, p.runtimeBinary = architecture, binary
	return p
}

type containerRuntime struct{ *Provider }

func (r containerRuntime) Arch(_ context.Context, _, declared string) (string, error) {
	if declared == "" {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.runtimeArch, nil
	}
	runs, _ := arch.GoArch(declared)
	return runs, nil
}

func (r containerRuntime) Binary(_ context.Context, architecture string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrappedFor = append(r.wrappedFor, architecture)
	return r.runtimeBinary, nil
}

func (p *Provider) WrappedFor() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.wrappedFor)
}

func (r containerRuntime) FunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	if source := r.stacks.imageSource(); source != nil {
		return source.Base(ctx, framework)
	}
	goarch, known := arch.GoArch(framework.Arch)
	if !known {
		goarch, _ = arch.GoArch(arch.X8664)
	}
	return images.PullNodeBase(ctx, PullStandInImage, goarch)
}

func (r containerRuntime) FunctionBuildPaths(buildoutput.Framework) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.buildPaths)
}

func (r containerRuntime) FunctionFiles(ctx context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	if source := r.stacks.imageSource(); source != nil {
		return source.Runtime(ctx, framework)
	}
	return map[string][]byte{images.RuntimeEntrypointFile: []byte(FunctionEntrypoint)}, nil
}
