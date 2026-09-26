package providerserver

import (
	"context"
	"fmt"
	"maps"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
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
	hooks provider.Hooks,
	entry provider.AppEntry,
	pack provider.PackAppResult,
	routing *provider.RoutingSpec,
) ([]provider.ImagePush, error) {
	if r.images == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s's functions are run from images, and nothing in this deploy names a registry to push them to", entry.App)
	}
	root := appbuild.ArtifactRoot()
	var pushes []provider.ImagePush
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
	hooks provider.Hooks,
	root string,
	entry provider.AppEntry,
	fn *contractv1.ManifestFunction,
	overlay map[string][]byte,
) (provider.ImagePush, error) {
	name := fn.GetLogicalName()
	dir, err := stagedDir(root, fn)
	if err != nil {
		return provider.ImagePush{}, err
	}
	framework := frameworkOf(fn)
	base, err := hooks.FunctionImages.ResolveBase(ctx, framework)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the base image %s's %s function is built on: %w", name, framework.Name, err)
	}
	files, err := runtimeOverlay(ctx, hooks, framework, name, overlay)
	if err != nil {
		return provider.ImagePush{}, err
	}
	image, err := images.FunctionImage(base, framework, dir, files)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	image, err = r.wrapFunction(ctx, name, framework, image)
	if err != nil {
		return provider.ImagePush{}, err
	}
	digest, err := image.Digest()
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	repository := functionRepository(entry.App, name)
	target := images.Ref(repository, naming.DigestTag(digest.String()), r.registry)
	return provider.ImagePush{
		App:      name,
		Source:   pinnedImageRef(target, digest.String()),
		ImageRef: target,
		Digest:   digest.String(),
		Function: true,
		Built:    image,
	}, nil
}

func (r *deployRun) wrapFunction(ctx context.Context, name string, framework appbuild.Framework, image v1.Image) (v1.Image, error) {
	goarch, known := arch.GoArch(framework.Arch)
	if !known {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s is built for %s, and this provider ships a container runtime for %s and %s alone",
			name, framework.Arch, arch.X8664, arch.ARM64)
	}
	body, err := r.provider.Runtime().Binary(ctx, goarch)
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

func runtimeOverlay(
	ctx context.Context,
	hooks provider.Hooks,
	framework appbuild.Framework,
	name string,
	overlay map[string][]byte,
) (map[string][]byte, error) {
	if !images.BootsThroughRuntime(framework) {
		return overlay, nil
	}
	body, err := hooks.FunctionImages.ReadRuntime(ctx, framework)
	if err != nil {
		return nil, fmt.Errorf("read the runtime %s boots through: %w", name, err)
	}
	if len(body) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this provider ships no runtime for a %s function to boot through, and %s is one", framework.Name, name)
	}
	files := make(map[string][]byte, len(overlay)+1)
	maps.Copy(files, overlay)
	files[images.NodeRuntimePath] = body
	return files, nil
}

func functionRepository(app, function string) string {
	if function == app {
		return app
	}
	return app + "-" + images.FunctionRoute(app, function)
}

func pinnedImageRef(target, digest string) string {
	repository := target[:strings.LastIndex(target, ":")]
	return repository + "@" + digest
}
