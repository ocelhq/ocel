package providerserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	built, err := images.InspectBuiltImage(ctx, repository, digest)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("inspect the image %s was built as: %w", app, err)
	}
	runs, err := runtimePort.Arch(ctx, app, entry.Arch)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the architecture %s's container runs on: %w", app, err)
	}
	if built.Architecture != runs {
		return provider.ImagePush{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s's image is built for %s and the target runs %s, which cannot execute it: build it for %s, and drop any --platform its Dockerfile pins a FROM to",
			app, images.ContainerPlatform(built.Architecture), images.ContainerPlatform(runs), images.ContainerPlatform(runs))
	}
	runtime, err := runtimePort.Binary(ctx, built.Architecture)
	if err != nil {
		return provider.ImagePush{}, fmt.Errorf("read the runtime %s's container boots through: %w", app, err)
	}
	if len(runtime) == 0 {
		return provider.ImagePush{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider ships no container runtime built for %s, and %s's image is built for it", built.Architecture, app)
	}
	next, err := r.readNextServerRuntime(ctx, entry)
	if err != nil {
		return provider.ImagePush{}, err
	}
	return provider.ImagePush{
		App:      app,
		Source:   ref,
		ImageRef: images.FormatRef(r.spec.Slug, repository, images.RuntimeTag(built.ContentDigest, runtime, next), r.registry),
		Wrap: func(ctx context.Context) (v1.Image, func(), error) {
			return images.WrapFromDaemon(ctx, repository, digest, runtime, next)
		},
	}, nil
}

func (r *deployRun) readNextServerRuntime(ctx context.Context, entry provider.AppEntry) (*images.NextServerRuntime, error) {
	read := r.provider.Hooks().ReadNextServerRuntime
	if read == nil || entry.Manifest.GetFramework().GetName() != buildoutput.FrameworkNext {
		return nil, nil
	}
	files, err := read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the Next server runtime %s's container loads: %w", entry.App, err)
	}
	return &images.NextServerRuntime{Files: files}, nil
}

func (r *deployRun) openImages(ctx context.Context, project *contractv1.ImageRegistry) error {
	registry, err := r.registryTarget(ctx, project)
	if err != nil {
		return err
	}
	r.registry = registry
	store, err := imageStoreFor(ctx, r.provider, r.registry)
	if err != nil {
		return err
	}
	r.images = store
	return nil
}

func registryTargetOf(project *contractv1.ImageRegistry) provider.RegistryTarget {
	return provider.RegistryTarget{
		Server:    project.GetServer(),
		Namespace: project.GetNamespace(),
		Username:  project.GetUsername(),
		Password:  project.GetPassword(),
	}
}

func removalImages(ctx context.Context, p provider.Provider, project *contractv1.ImageRegistry, log progress.Log) provider.ImageStore {
	if project.GetServer() == "" {
		return nil
	}
	store, err := imageStoreFor(ctx, p, registryTargetOf(project))
	if err != nil {
		log.Warn(fmt.Sprintf("Left the images this project pushed to %s in place, as the registry could not be opened: %v", project.GetServer(), err))
		return nil
	}
	return store
}

func (r *deployRun) registryTarget(ctx context.Context, project *contractv1.ImageRegistry) (provider.RegistryTarget, error) {
	if project.GetServer() != "" {
		return registryTargetOf(project), nil
	}
	ensure := r.provider.Hooks().EnsureImageRegistry
	if ensure == nil || len(r.spec.Apps) == 0 {
		return provider.RegistryTarget{}, nil
	}
	own, err := ensure(ctx, r.spec.Tier)
	if err != nil {
		return provider.RegistryTarget{}, fmt.Errorf("resolve the registry this provider hosts: %w", err)
	}
	if !own.Named() && own != (provider.RegistryTarget{}) {
		return provider.RegistryTarget{}, errors.New("the provider answered an image registry with no server, which names nowhere to push to")
	}
	return own, nil
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

func pushRemovedImages(ctx context.Context, images provider.ImagePushes, log progress.Log) error {
	if images.Store == nil {
		return nil
	}
	var removed []provider.ImagePush
	for _, push := range images.Pushes {
		present, err := images.Store.Has(ctx, push)
		if err != nil {
			log.Warn(fmt.Sprintf("Could not confirm %s still holds %s after its release provisioned, so a removal that ran meanwhile would go unnoticed: %v",
				images.Store.Destination(), push.ImageRef, err))
			continue
		}
		if !present {
			log.Warn(fmt.Sprintf("%s left %s while its release provisioned, and is sent again so the release can still start new tasks", push.ImageRef, images.Store.Destination()))
			removed = append(removed, push)
		}
	}
	return provider.ImagePushes{Store: images.Store, Pushes: removed}.PushMissing(ctx, log)
}
