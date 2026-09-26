package fake

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
)

const RuntimeBinary = "the fake container runtime"

func (p *Provider) WrappingContainers(arch string, binary []byte) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runtimeArch, p.runtimeBinary = arch, binary
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

func (r containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrappedFor = append(r.wrappedFor, arch)
	return r.runtimeBinary, nil
}

func (p *Provider) WrappedFor() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.wrappedFor)
}
