package release

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	blobsDir              = "blobs/sha256/"
	containerdImageName   = "io.containerd.image.name"
	ociImageRefName       = "org.opencontainers.image.ref.name"
	dockerHubRegistryName = "docker.io"
)

func WriteArchive(ref name.Tag, built v1.Image, to io.Writer) error {
	archive := tar.NewWriter(to)
	config, err := built.ConfigName()
	if err != nil {
		return err
	}
	rawConfig, err := built.RawConfigFile()
	if err != nil {
		return err
	}
	if err := writeFile(archive, blobsDir+config.Hex, rawConfig); err != nil {
		return err
	}
	layers, err := writeLayers(archive, built)
	if err != nil {
		return err
	}
	manifest, err := built.Digest()
	if err != nil {
		return err
	}
	rawManifest, err := built.RawManifest()
	if err != nil {
		return err
	}
	mediaType, err := built.MediaType()
	if err != nil {
		return err
	}
	if err := writeFile(archive, blobsDir+manifest.Hex, rawManifest); err != nil {
		return err
	}
	index, err := json.Marshal(v1.IndexManifest{
		SchemaVersion: 2,
		MediaType:     types.OCIImageIndex,
		Manifests: []v1.Descriptor{{
			MediaType: mediaType,
			Size:      int64(len(rawManifest)),
			Digest:    manifest,
			Annotations: map[string]string{
				containerdImageName: normalizedName(ref),
				ociImageRefName:     ref.TagStr(),
			},
		}},
	})
	if err != nil {
		return err
	}
	if err := writeFile(archive, "index.json", index); err != nil {
		return err
	}
	described, err := json.Marshal(tarball.Manifest{{
		Config:   blobsDir + config.Hex,
		RepoTags: []string{ref.String()},
		Layers:   layers,
	}})
	if err != nil {
		return err
	}
	if err := writeFile(archive, "manifest.json", described); err != nil {
		return err
	}
	if err := writeFile(archive, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}
	return archive.Close()
}

func writeLayers(archive *tar.Writer, built v1.Image) ([]string, error) {
	layers, err := built.Layers()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(layers))
	written := map[v1.Hash]bool{}
	for _, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return nil, err
		}
		path := blobsDir + digest.Hex
		paths = append(paths, path)
		if written[digest] {
			continue
		}
		written[digest] = true
		if err := writeLayer(archive, path, layer); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func writeLayer(archive *tar.Writer, path string, layer v1.Layer) error {
	size, err := layer.Size()
	if err != nil {
		return err
	}
	blob, err := layer.Compressed()
	if err != nil {
		return err
	}
	defer func() { _ = blob.Close() }()
	if err := archive.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	copied, err := io.Copy(archive, blob)
	if err != nil {
		return err
	}
	if copied != size {
		return fmt.Errorf("layer %s is %d bytes, not the %d its manifest names", path, copied, size)
	}
	return nil
}

func writeFile(archive *tar.Writer, path string, body []byte) error {
	if err := archive.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := archive.Write(body)
	return err
}

func normalizedName(ref name.Tag) string {
	registry := ref.RegistryStr()
	if registry == name.DefaultRegistry {
		registry = dockerHubRegistryName
	}
	return registry + "/" + ref.RepositoryStr() + ":" + ref.TagStr()
}
