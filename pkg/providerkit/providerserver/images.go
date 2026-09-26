package providerserver

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func imageStoreFor(ctx context.Context, p provider.Provider, target provider.RegistryTarget) (provider.ImageStore, error) {
	hooks := p.Hooks()
	if !target.Named() {
		if hooks.OpenDirectImages == nil {
			return nil, nil
		}
		return hooks.OpenDirectImages(ctx)
	}
	if hooks.OpenRegistryImages != nil {
		return hooks.OpenRegistryImages(ctx, target)
	}
	return images.RegistryStore(target), nil
}

func (r *deployRun) wrappedPush(ctx context.Context, entry provider.AppEntry) (provider.ImagePush, error) {
	runtimePort := r.provider.Runtime()
	app, ref := entry.App, entry.Image
	repository, digest, pinned := strings.Cut(ref, "@")
	if !pinned || repository == "" || digest == "" {
		return provider.ImagePush{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s names the image %q, which pins no digest, so there is nothing to push under a coordinate", app, ref)
	}
	arch, err := images.BuiltArchitecture(ctx, repository, digest)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the architecture %s's image is built for: %w", app, err)
	}
	runs, err := runtimePort.Arch(ctx, app, entry.Arch)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the architecture %s's container runs on: %w", app, err)
	}
	if arch != runs {
		return provider.ImagePush{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s's image is built for %s and the target runs %s, which cannot execute it: build it for %s, and drop any --platform its Dockerfile pins a FROM to",
			app, images.ContainerPlatform(arch), images.ContainerPlatform(runs), images.ContainerPlatform(runs))
	}
	runtime, err := runtimePort.Binary(ctx, arch)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the runtime %s's container boots through: %w", app, err)
	}
	if len(runtime) == 0 {
		return provider.ImagePush{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider ships no container runtime built for %s, and %s's image is built for it", arch, app)
	}
	return provider.ImagePush{
		App:      app,
		Source:   ref,
		ImageRef: images.Ref(repository, images.RuntimeTag(digest, runtime), r.registry),
		Wrap: func(ctx context.Context) (v1.Image, func(), error) {
			return images.WrapFromDaemon(ctx, repository, digest, runtime)
		},
	}, nil
}

func (r *deployRun) openImages(ctx context.Context, wired *contractv1.ImageRegistry) error {
	r.registry = provider.RegistryTarget{
		Server:    wired.GetServer(),
		Namespace: wired.GetNamespace(),
		Username:  wired.GetUsername(),
		Password:  wired.GetPassword(),
	}
	store, err := imageStoreFor(ctx, r.provider, r.registry)
	if err != nil {
		return err
	}
	r.images = store
	return nil
}

func (r *deployRun) containerPush(ctx context.Context, entry provider.AppEntry) (provider.ImagePush, error) {
	return r.wrappedPush(ctx, entry)
}

func (r *deployRun) imagePushes(ctx context.Context, entry provider.AppEntry, functions []provider.ImagePush) (provider.ImagePushes, error) {
	if len(functions) > 0 {
		return provider.ImagePushes{Store: r.images, Pushes: functions}, nil
	}
	if entry.Compute() != provider.ComputeContainer || entry.Image == "" {
		return provider.ImagePushes{}, nil
	}
	if r.images == nil {
		return provider.ImagePushes{}, refusal.Refuse(refusal.CodeInvalid,
			"%s runs as a container, and this provider is served by pulling its image from a registry rather than being handed one: "+
				"nothing names a registry, so the image has nowhere to go and the machine has nowhere to pull it from.\n"+
				"    → name a `registry` in the project config, with `password` set to the name of the environment variable that contains the token",
			entry.App)
	}
	push, err := r.containerPush(ctx, entry)
	if err != nil {
		return provider.ImagePushes{}, err
	}
	return provider.ImagePushes{Store: r.images, Pushes: []provider.ImagePush{push}}, nil
}
