package fake

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/images"
)

const distrolessPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func PullStandInImage(_ context.Context, ref, goarch string) (v1.Image, error) {
	if ref != images.NodeBase {
		return nil, fmt.Errorf("the fake holds a stand-in for %s alone, not %s", images.NodeBase, ref)
	}
	node := path.Join(images.NodeBinDir, "node")
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	body := []byte("#!/bin/sh\n")
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.TrimPrefix(node, "/"), Mode: 0o755, Size: int64(len(body))}); err != nil {
		return nil, err
	}
	if _, err := archive.Write(body); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(packed.Bytes())), nil })
	if err != nil {
		return nil, err
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, err
	}
	file, err := image.ConfigFile()
	if err != nil {
		return nil, err
	}
	file = file.DeepCopy()
	file.OS, file.Architecture = "linux", goarch
	file.Config.Entrypoint = []string{node}
	file.Config.Env = []string{distrolessPath}
	return mutate.ConfigFile(image, file)
}
