package gcp

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/environment"
)

const binaryImageTagLen = 32

func binaryImageTag(payload func() []byte) func() string {
	return sync.OnceValue(func() string {
		sum := sha256.New()
		sum.Write([]byte(staticImage + "\x00"))
		sum.Write(payload())
		return hex.EncodeToString(sum.Sum(nil))[:binaryImageTagLen]
	})
}

func (p *Provider) pushBinary(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error {
	base, err := p.based(ctx, staticImage)
	if err != nil {
		return err
	}
	built, err := binaryImage(base, binary, path)
	if err != nil {
		return err
	}
	return p.pushImage(ctx, tier, name, ref, built, nil)
}

func binaryImage(base v1.Image, binary []byte, path string) (v1.Image, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     path[1:],
		Mode:     0o755,
		Size:     int64(len(binary)),
	}); err != nil {
		return nil, fmt.Errorf("open the %s image layer: %w", path, err)
	}
	if _, err := archive.Write(binary); err != nil {
		return nil, fmt.Errorf("write %s into its image layer: %w", path, err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close the %s image layer: %w", path, err)
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
	config.Entrypoint = []string{path}
	config.Cmd = nil
	return mutate.Config(appended, config)
}
