package gcp

import (
	"context"
	"fmt"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) pushImage(ctx context.Context, class edge.Class, app, ref string, built v1.Image, progress edge.Progress) error {
	digest, err := built.Digest()
	if err != nil {
		return fmt.Errorf("read the digest of the %s image: %w", app, err)
	}
	store, err := p.imageStore(ctx, class)
	if err != nil {
		return err
	}
	push := provider.ImagePush{App: app, ImageRef: ref, Digest: digest.String(), Built: built}
	present, err := store.Has(ctx, push)
	if err != nil || present {
		return err
	}
	progress = ensureProgress(progress)
	progress.Say("Pushing " + app + "'s image to " + ref)
	return store.Push(ctx, push, progress)
}

func (p *Provider) imageStore(ctx context.Context, class edge.Class) (provider.ImageStore, error) {
	if p.emulated() {
		return p.OpenDirectImages(ctx)
	}
	at, err := p.EnsureImageRegistry(ctx, class, nil)
	if err != nil {
		return nil, err
	}
	return images.RegistryStore(at), nil
}
