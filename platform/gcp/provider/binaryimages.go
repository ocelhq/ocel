package gcp

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func binaryImage(base v1.Image, binary []byte, at string) (v1.Image, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     at[1:],
		Mode:     0o755,
		Size:     int64(len(binary)),
	}); err != nil {
		return nil, fmt.Errorf("open the %s image layer: %w", at, err)
	}
	if _, err := archive.Write(binary); err != nil {
		return nil, fmt.Errorf("write %s into its image layer: %w", at, err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close the %s image layer: %w", at, err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed.Bytes())), nil
	})
	if err != nil {
		return nil, err
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		return nil, err
	}
	file, err := appended.ConfigFile()
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Entrypoint = []string{at}
	config.Cmd = nil
	return mutate.Config(appended, config)
}

func (p *Provider) pushImage(ctx context.Context, class providerkit.Class, app, ref string, built v1.Image, report providerkit.Reporter) error {
	digest, err := built.Digest()
	if err != nil {
		return fmt.Errorf("read the digest of the %s image: %w", app, err)
	}
	store, err := p.imageStoreFor(ctx, class)
	if err != nil {
		return err
	}
	push := providerkit.ImagePush{App: app, Target: ref, Digest: digest.String(), Built: built}
	held, err := store.Has(ctx, push)
	if err != nil || held {
		return err
	}
	if report != nil {
		report.Say("Pushing the " + app + " image to " + ref)
	}
	return store.Push(ctx, push, report)
}

func (p *Provider) imageStoreFor(ctx context.Context, class providerkit.Class) (providerkit.ImageStore, error) {
	if p.emulated() {
		return p.DirectImages(ctx)
	}
	at, err := p.ImageRegistry(ctx, class, nil)
	if err != nil {
		return nil, err
	}
	return p.Images(ctx, at)
}
