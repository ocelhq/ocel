package providerkit

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type FunctionImager interface {
	FunctionBase(ctx context.Context, runtime Runtime) (v1.Image, error)
}

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
	imager FunctionImager,
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
		push, err := r.imageFunction(ctx, imager, root, entry, fn, overlayFor(pack.Overlay, fn, routing))
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
	imager FunctionImager,
	root string,
	entry AppEntry,
	fn *contractv1.ManifestFunction,
	overlay map[string][]byte,
) (ImagePush, error) {
	name := fn.GetLogicalName()
	if fn.GetArtifactPath() == "" {
		return ImagePush{}, Refuse(CodeInvalid, "function %s names no build artifact, so there is nothing to ship", name)
	}
	runtime := Runtime{Name: fn.GetRuntime().GetName(), Arch: fn.GetRuntime().GetArch()}
	base, err := imager.FunctionBase(ctx, runtime)
	if err != nil {
		return ImagePush{}, fmt.Errorf("read the base image %s's %s function is built on: %w", name, runtime.Name, err)
	}
	image, err := FunctionImage(base, filepath.Join(root, filepath.FromSlash(fn.GetArtifactPath())), overlay)
	if err != nil {
		return ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	digest, err := image.Digest()
	if err != nil {
		return ImagePush{}, fmt.Errorf("build %s's image: %w", name, err)
	}
	repository := functionRepository(entry.App, name)
	target := coordinate(repository, naming.DigestTag(digest.String()), r.registry)
	return ImagePush{
		App:      name,
		Source:   pinnedCoordinate(target, digest.String()),
		Target:   target,
		Digest:   digest.String(),
		Function: true,
		Built:    image,
	}, nil
}

func functionRepository(app, function string) string {
	if function == app {
		return app
	}
	return app + "-" + function
}

func pinnedCoordinate(target, digest string) string {
	repository := target[:strings.LastIndex(target, ":")]
	return repository + "@" + digest
}
