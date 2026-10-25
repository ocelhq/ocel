package fake

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
)

func (p *Provider) RunningFunctionsAsImages(
	base func(ctx context.Context, framework buildoutput.Framework) (v1.Image, error),
	runtime func(ctx context.Context, framework buildoutput.Framework) (map[string][]byte, error),
) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runsFunctionsAsImages = true
	p.stacks.mu.Lock()
	p.stacks.images = &functioncode.ImageSource{Base: base, Runtime: runtime, Binary: containerRuntime{p}.Binary}
	p.stacks.mu.Unlock()
	return p
}

func (r *Stacks) imageSource() *functioncode.ImageSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.images
}

func (r *Stacks) withFunctionImages(ctx context.Context, spec provider.StackSpec) (provider.StackSpec, bool, error) {
	source := r.imageSource()
	if source == nil || spec.App == nil || spec.App.Compute != provider.ComputeServerless {
		return spec, false, nil
	}
	pushes, err := functioncode.FunctionImages(ctx, spec, *source)
	if err != nil {
		return provider.StackSpec{}, true, err
	}
	spec.Images.Pushes = append(append([]provider.ImagePush(nil), spec.Images.Pushes...), pushes...)
	return spec, true, nil
}
