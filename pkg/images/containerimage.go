package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	runtimeTagHexLen = 12
	wrappedBaseLabel = "ocel.base"
)

func ContainerPlatform(arch string) string { return "linux/" + arch }

func WrapContainer(base v1.Image, runtime []byte) (v1.Image, error) {
	file, err := base.ConfigFile()
	if err != nil {
		return nil, err
	}
	command := append(append([]string{}, file.Config.Entrypoint...), file.Config.Cmd...)
	if len(command) == 0 {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the image names neither an ENTRYPOINT nor a CMD, so there is nothing for the runtime to run in front of: give it one")
	}
	if len(runtime) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady, "this provider ships no runtime built for %s", file.Architecture)
	}
	config := file.Config
	packed, err := packRuntimeLayer(runtime)
	if err != nil {
		return nil, err
	}
	layer, err := newBytesLayer(packed)
	if err != nil {
		return nil, err
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		return nil, err
	}
	digest, err := base.Digest()
	if err != nil {
		return nil, err
	}
	config.Labels = maps.Clone(config.Labels)
	if config.Labels == nil {
		config.Labels = map[string]string{}
	}
	config.Labels[wrappedBaseLabel] = digest.String()
	config.Entrypoint = []string{containerimage.RuntimePath}
	config.Cmd = command
	return mutate.Config(appended, config)
}

func newBytesLayer(packed []byte) (v1.Layer, error) {
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed)), nil
	})
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func packRuntimeLayer(runtime []byte) ([]byte, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(containerimage.RuntimePath[:strings.LastIndex(containerimage.RuntimePath, "/")], "/") + "/",
		Mode:     0o755,
	}); err != nil {
		return nil, err
	}
	if err := tarBody(archive, containerimage.RuntimePath, runtime, 0o755); err != nil {
		return nil, err
	}
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(containerimage.LivePath, "/") + "/",
		Mode:     0o1777,
	}); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
}

func RuntimeTag(contentDigest string, runtime []byte) string {
	hash := sha256.Sum256(runtime)
	return naming.DigestTag(contentDigest) + naming.WordSeparator + "ocel" + naming.WordSeparator + hex.EncodeToString(hash[:])[:runtimeTagHexLen]
}

func InspectBuiltImage(ctx context.Context, repository, digest string) (ImageInspection, error) {
	host, err := DockerHostFromEnv()
	if err != nil {
		return ImageInspection{}, err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Inspect(ctx, &http.Client{Transport: transport}, repository+":"+naming.DigestTag(digest))
}

func WrapFromDaemon(ctx context.Context, repository, digest string, runtime []byte) (v1.Image, func(), error) {
	host, err := DockerHostFromEnv()
	if err != nil {
		return nil, nil, err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()

	ref := repository + ":" + naming.DigestTag(digest)
	stream, err := host.Export(ctx, &http.Client{Transport: transport}, ref)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = stream.Close() }()

	saved, err := os.CreateTemp("", "ocel-image-")
	if err != nil {
		return nil, nil, err
	}
	discard := func() { _ = os.Remove(saved.Name()) }
	checked := NewVerifiedExport(stream, host.Address, ref)
	_, copyErr := io.Copy(saved, checked)
	closeErr := saved.Close()
	if incomplete := checked.VerifyErr(); incomplete != nil {
		discard()
		return nil, nil, incomplete
	}
	if copyErr != nil || closeErr != nil {
		discard()
		return nil, nil, fmt.Errorf("save %s out of the daemon at %s: %w", ref, host.Address, cmpErr(copyErr, closeErr))
	}

	base, err := tarball.ImageFromPath(saved.Name(), nil)
	if err != nil {
		discard()
		return nil, nil, fmt.Errorf("read %s as the daemon at %s exported it: %w", ref, host.Address, err)
	}
	wrapped, err := WrapContainer(base, runtime)
	if err != nil {
		discard()
		return nil, nil, err
	}
	return wrapped, discard, nil
}

func cmpErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
