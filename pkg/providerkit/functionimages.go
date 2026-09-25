package providerkit

import (
	"context"
	"fmt"
	"maps"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (r *deployRun) recordFunctionImage(logical, ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.functionImages[logical] = ref
}

func (r *deployRun) functionImage(logical string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.functionImages[logical]
}

func (r *deployRun) imageFunctions(
	ctx context.Context,
	hooks Hooks,
	entry AppEntry,
	pack AppPack,
	routing *RoutingPlan,
) ([]ImagePush, error) {
	if r.images == nil {
		return nil, Refuse(CodeInvalid,
			"%s's functions are run from images, and nothing this deploy carries names a registry to push them to", entry.App)
	}
	root := ArtifactRoot()
	var pushes []ImagePush
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != entry.App {
			continue
		}
		push, err := r.imageFunction(ctx, hooks, root, entry, fn, overlayFor(pack.Overlay, fn, routing))
		if err != nil {
			return nil, err
		}
		r.recordFunctionImage(fn.GetLogicalName(), push.Source)
		pushes = append(pushes, push)
	}
	return pushes, nil
}

func (r *deployRun) imageFunction(
	ctx context.Context,
	hooks Hooks,
	root string,
	entry AppEntry,
	fn *contractv1.ManifestFunction,
	overlay map[string][]byte,
) (ImagePush, error) {
	name := fn.GetLogicalName()
	dir, err := stagedDir(root, fn)
	if err != nil {
		return ImagePush{}, err
	}
	framework := Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()}
	base, err := hooks.FunctionBaseImage(ctx, framework)
	if err != nil {
		return ImagePush{}, fmt.Errorf("read the base image %s's %s function is built on: %w", name, framework.Name, err)
	}
	carried, err := runtimeOverlay(ctx, hooks, framework, name, overlay)
	if err != nil {
		return ImagePush{}, err
	}
	image, err := FunctionImage(base, framework, dir, carried)
	if err != nil {
		return ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	image, err = r.wrapFunction(ctx, name, framework, image)
	if err != nil {
		return ImagePush{}, err
	}
	digest, err := image.Digest()
	if err != nil {
		return ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	repository := functionRepository(entry.App, name)
	target := imageRef(repository, naming.DigestTag(digest.String()), r.registry)
	return ImagePush{
		App:      name,
		Source:   pinnedImageRef(target, digest.String()),
		ImageRef: target,
		Digest:   digest.String(),
		Function: true,
		Built:    image,
	}, nil
}

func (r *deployRun) wrapFunction(ctx context.Context, name string, framework Framework, image v1.Image) (v1.Image, error) {
	arch, known := GoArch(framework.Arch)
	if !known {
		return nil, Refuse(CodeInvalid,
			"%s is built for %s, and this provider carries a container runtime for %s and %s alone",
			name, framework.Arch, ArchX8664, ArchARM64)
	}
	body, err := r.provider.Runtime().Binary(ctx, arch)
	if err != nil {
		return nil, fmt.Errorf("read the runtime %s's function boots through: %w", name, err)
	}
	if len(body) == 0 {
		return nil, Refuse(CodeNotReady,
			"this provider carries no container runtime built for %s, and %s is built for it", arch, name)
	}
	wrapped, err := WrapContainer(image, body)
	if err != nil {
		return nil, fmt.Errorf("wrap %s's image in the runtime: %w", name, err)
	}
	return wrapped, nil
}

func runtimeOverlay(
	ctx context.Context,
	hooks Hooks,
	framework Framework,
	name string,
	overlay map[string][]byte,
) (map[string][]byte, error) {
	if !BootsThroughRuntime(framework) {
		return overlay, nil
	}
	body, err := hooks.FunctionRuntime(ctx, framework)
	if err != nil {
		return nil, fmt.Errorf("read the runtime %s boots through: %w", name, err)
	}
	if len(body) == 0 {
		return nil, Refuse(CodeNotReady,
			"this provider carries no runtime for a %s function to boot through, and %s is one", framework.Name, name)
	}
	carried := make(map[string][]byte, len(overlay)+1)
	maps.Copy(carried, overlay)
	carried[NodeRuntimePath] = body
	return carried, nil
}

func FunctionRoute(app, function string) string {
	lead := naming.Join(naming.FieldSeparator, string(naming.KindFunction), app) + naming.FieldSeparator
	return naming.Sanitize(strings.TrimPrefix(function, lead))
}

func functionRepository(app, function string) string {
	if function == app {
		return app
	}
	return app + "-" + FunctionRoute(app, function)
}

func pinnedImageRef(target, digest string) string {
	repository := target[:strings.LastIndex(target, ":")]
	return repository + "@" + digest
}
