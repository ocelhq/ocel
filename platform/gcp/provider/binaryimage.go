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

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) pushBinary(ctx context.Context, class edge.Class, name, ref string, binary []byte, path string) error {
	base, err := p.based(ctx, staticImage)
	if err != nil {
		return err
	}
	built, err := binaryImage(base, binary, path)
	if err != nil {
		return err
	}
	return p.pushImage(ctx, class, name, ref, built, nil)
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
