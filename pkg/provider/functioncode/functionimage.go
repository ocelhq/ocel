package functioncode

import (
	"context"
	"fmt"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type ImageSource struct {
	Base    func(ctx context.Context, framework buildoutput.Framework) (v1.Image, error)
	Runtime func(ctx context.Context, framework buildoutput.Framework) (map[string][]byte, error)
	Binary  func(ctx context.Context, arch string) ([]byte, error)
}

func FunctionImages(ctx context.Context, spec provider.StackSpec, source ImageSource) ([]provider.ImagePush, error) {
	if spec.App == nil || len(spec.App.Functions) == 0 {
		return nil, nil
	}
	if spec.Images.Store == nil && !slices.ContainsFunc(spec.App.Functions, func(fn provider.FunctionSpec) bool { return FunctionImageRef(spec.Images, fn.Name) == "" }) {
		return nil, nil
	}
	if spec.Images.Store == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s's functions are run from images, and nothing in this deploy names a registry to push them to", spec.App.App)
	}
	pushes := make([]provider.ImagePush, 0, len(spec.App.Functions))
	for _, fn := range spec.App.Functions {
		if FunctionImageRef(spec.Images, fn.Name) != "" {
			continue
		}
		image, err := functionImage(ctx, fn, source)
		if err != nil {
			return nil, err
		}
		push, err := newImagePush(spec.Ref.Project, functionRepository(spec.App.App, fn.Name), spec.Images.Registry, image)
		if err != nil {
			return nil, fmt.Errorf("build %s's image: %w", fn.Name, err)
		}
		push.App, push.Function = fn.Name, true
		pushes = append(pushes, push)
	}
	return pushes, nil
}

func FunctionImageRef(images provider.ImagePushes, function string) string {
	for _, push := range images.Pushes {
		if push.Function && push.App == function {
			return push.ImageRef
		}
	}
	return ""
}

func ContainerImage(ctx context.Context, project, app string, registry provider.RegistryTarget, fn provider.FunctionSpec, source ImageSource) (provider.ImagePush, error) {
	image, err := functionImage(ctx, fn, source)
	if err != nil {
		return provider.ImagePush{}, err
	}
	push, err := newImagePush(project, app, registry, image)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("build %s's image: %w", app, err)
	}
	push.App = app
	return push, nil
}

func newImagePush(project, repository string, registry provider.RegistryTarget, image v1.Image) (provider.ImagePush, error) {
	digest, err := image.Digest()
	if err != nil {
		return provider.ImagePush{}, err
	}
	return provider.ImagePush{
		ImageRef: images.FormatRef(project, repository, naming.DigestTag(digest.String()), registry),
		Digest:   digest.String(),
		Built:    image,
	}, nil
}

func functionImage(ctx context.Context, fn provider.FunctionSpec, source ImageSource) (v1.Image, error) {
	dir, err := functionDir(fn)
	if err != nil {
		return nil, err
	}
	base, err := source.Base(ctx, fn.Framework)
	if err != nil {
		return nil, fmt.Errorf("read the base image %s's %s function is built on: %w", fn.Name, fn.Framework.Name, err)
	}
	runtime, err := readRuntime(ctx, source, fn.Framework, fn.Name)
	if err != nil {
		return nil, err
	}
	image, err := images.FunctionImage(base, fn.Framework, dir, images.FunctionImageOptions{
		Overlay: fn.Overlay,
		Runtime: runtime,
	})
	if err != nil {
		return nil, fmt.Errorf("build %s's image: %w", fn.Name, err)
	}
	return wrapFunction(ctx, source, fn.Name, fn.Framework, image)
}

func wrapFunction(ctx context.Context, source ImageSource, name string, framework buildoutput.Framework, image v1.Image) (v1.Image, error) {
	goarch, known := arch.GoArch(framework.Arch)
	if !known {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s is built for %s, and this provider ships a container runtime for %s and %s alone",
			name, framework.Arch, arch.X8664, arch.ARM64)
	}
	body, err := source.Binary(ctx, goarch)
	if err != nil {
		return nil, fmt.Errorf("read the runtime %s's function boots through: %w", name, err)
	}
	if len(body) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this provider ships no container runtime built for %s, and %s is built for it", goarch, name)
	}
	wrapped, err := images.WrapContainer(image, body)
	if err != nil {
		return nil, fmt.Errorf("wrap %s's image in the runtime: %w", name, err)
	}
	return wrapped, nil
}

func readRuntime(ctx context.Context, source ImageSource, framework buildoutput.Framework, name string) (map[string][]byte, error) {
	if !images.BootsThroughRuntime(framework) {
		return nil, nil
	}
	files, err := source.Runtime(ctx, framework)
	if err != nil {
		return nil, fmt.Errorf("read the runtime %s boots through: %w", name, err)
	}
	if len(files) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this provider ships no runtime for a %s function to boot through, and %s is one", framework.Name, name)
	}
	if _, ok := files[images.RuntimeEntrypointFile]; !ok {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"this provider's runtime for a %s function holds no %s, so %s would boot node over a file that is not there", framework.Name, images.RuntimeEntrypointFile, name)
	}
	return files, nil
}

func functionRepository(app, function string) string {
	if function == app {
		return app
	}
	return app + "-" + images.FunctionRoute(app, function)
}
