package functions

import (
	"context"
	"maps"
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

const pythonImage = "gcr.io/distroless/python3-debian13@sha256:f2b206661cee3edb44f132d7f054a9ced96f671d8a973de0db750895c9acb2fb"

type Images struct {
	based func(ctx context.Context, ref string) (v1.Image, error)
	bases map[string]base
}

func NewImages(based func(ctx context.Context, ref string) (v1.Image, error)) Images {
	return Images{based: based, bases: functionBases()}
}

type base struct {
	ref  string
	bins []string
}

func functionBases() map[string]base {
	return map[string]base{
		buildoutput.FrameworkNode:      {ref: images.NodeBase, bins: []string{images.NodeBinDir}},
		buildoutput.FrameworkNext:      {ref: images.NodeBase, bins: []string{images.NodeBinDir}},
		buildoutput.FrameworkSvelteKit: {ref: images.NodeBase, bins: []string{images.NodeBinDir}},
		buildoutput.FrameworkGo:        {ref: run.StaticImage},
		buildoutput.FrameworkPython:    {ref: pythonImage},
		buildoutput.FrameworkRust:      {ref: run.StaticImage},
	}
}

func (p Images) ResolveBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	if err := run.RefuseNotX8664(framework.Arch, "the "+framework.Name+" function"); err != nil {
		return nil, err
	}
	on, shipped := p.bases[framework.Name]
	if !shipped {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a function on Cloud Run is a container, and this provider ships no base image a %s function could run in: it ships one for %s",
			framework.Name, strings.Join(slices.Sorted(maps.Keys(p.bases)), ", "))
	}
	image, err := p.based(ctx, on.ref)
	if err != nil {
		return nil, err
	}
	return images.NewCommandImage(image, on.bins)
}

func (p Images) ReadRuntime(_ context.Context, framework buildoutput.Framework) (map[string][]byte, error) {
	switch framework.Name {
	case buildoutput.FrameworkNode, buildoutput.FrameworkSvelteKit:
		return map[string][]byte{images.RuntimeEntrypointFile: payloads.NodeRuntime()}, nil
	case buildoutput.FrameworkNext:
		return maps.Clone(payloads.NextRuntime()), nil
	default:
		return nil, nil
	}
}

func (p Images) WithImages(ctx context.Context, spec provider.StackSpec) (provider.StackSpec, error) {
	if spec.App == nil || spec.App.Compute != provider.ComputeServerless {
		return spec, nil
	}
	pushes, err := functioncode.FunctionImages(ctx, spec, functioncode.ImageSource{
		Base:    p.ResolveBase,
		Runtime: p.ReadRuntime,
		Binary:  containerBinary,
	})
	if err != nil {
		return provider.StackSpec{}, err
	}
	spec.Images.Pushes = append(slices.Clone(spec.Images.Pushes), pushes...)
	return spec, nil
}

func containerBinary(_ context.Context, architecture string) ([]byte, error) {
	return payloads.ContainerRuntime(architecture)
}
