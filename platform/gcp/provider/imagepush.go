package gcp

import (
	"context"
	"fmt"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func (p *Provider) pushImage(ctx context.Context, tier environment.Tier, app, ref string, built v1.Image, progress progress.Log) error {
	digest, err := built.Digest()
	if err != nil {
		return fmt.Errorf("read the digest of the %s image: %w", app, err)
	}
	store, err := p.imageStore(ctx, tier)
	if err != nil {
		return err
	}
	push := provider.ImagePush{App: app, ImageRef: ref, Digest: digest.String(), Built: built}
	present, err := store.Has(ctx, push)
	if err != nil || present {
		return err
	}
	progress = gcloud.EnsureProgress(progress)
	progress.Say("Pushing " + app + "'s image to " + ref)
	return store.Push(ctx, push, progress)
}

func (p *Provider) imageStore(ctx context.Context, tier environment.Tier) (provider.ImageStore, error) {
	if p.emulated() {
		return p.OpenDirectImages(ctx)
	}
	at, err := p.EnsureImageRegistry(ctx, tier)
	if err != nil {
		return nil, err
	}
	return images.RegistryStore(at), nil
}

func (p *Provider) pushBinary(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error {
	base, err := p.images.Based(ctx, run.StaticImage)
	if err != nil {
		return err
	}
	built, err := run.BinaryImage(base, binary, path)
	if err != nil {
		return err
	}
	return p.pushImage(ctx, tier, name, ref, built, nil)
}
