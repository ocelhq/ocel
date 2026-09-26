package providerkit

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func (r *deployRun) wrappedPush(ctx context.Context, entry AppEntry) (images.ImagePush, error) {
	runtimePort := r.provider.Runtime()
	app, ref := entry.App, entry.Image
	repository, digest, pinned := strings.Cut(ref, "@")
	if !pinned || repository == "" || digest == "" {
		return images.ImagePush{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s carries the image %q, which pins no digest, so there is nothing to push under a coordinate", app, ref)
	}
	arch, err := images.BuiltArchitecture(ctx, repository, digest)
	if err != nil {
		return images.ImagePush{}, fmt.Errorf("read the architecture %s's image is built for: %w", app, err)
	}
	runs, err := runtimePort.Arch(ctx, app, entry.Arch)
	if err != nil {
		return images.ImagePush{}, fmt.Errorf("read the architecture %s's container runs on: %w", app, err)
	}
	if arch != runs {
		return images.ImagePush{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s's image is built for %s and the target runs %s, which cannot execute it: build it for %s, and drop any --platform its Dockerfile pins a FROM to",
			app, images.ContainerPlatform(arch), images.ContainerPlatform(runs), images.ContainerPlatform(runs))
	}
	runtime, err := runtimePort.Binary(ctx, arch)
	if err != nil {
		return images.ImagePush{}, fmt.Errorf("read the runtime %s's container boots through: %w", app, err)
	}
	if len(runtime) == 0 {
		return images.ImagePush{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider carries no container runtime built for %s, and %s's image is built for it", arch, app)
	}
	return images.ImagePush{
		App:      app,
		Source:   ref,
		ImageRef: images.ImageRef(repository, images.RuntimeTag(digest, runtime), r.registry),
		Wrap: func(ctx context.Context) (v1.Image, func(), error) {
			return images.WrapFromDaemon(ctx, repository, digest, runtime)
		},
	}, nil
}
