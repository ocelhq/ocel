package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const runtimeTagHexLen = 12

type Runtime interface {
	Arch(ctx context.Context, app, declared string) (string, error)
	Binary(ctx context.Context, arch string) ([]byte, error)
}

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
	packed, err := runtimeLayer(runtime)
	if err != nil {
		return nil, err
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed)), nil
	})
	if err != nil {
		return nil, err
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Entrypoint = []string{appbuild.ContainerRuntimePath}
	config.Cmd = command
	return mutate.Config(appended, config)
}

func runtimeLayer(runtime []byte) ([]byte, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(appbuild.ContainerRuntimePath[:strings.LastIndex(appbuild.ContainerRuntimePath, "/")], "/") + "/",
		Mode:     0o755,
	}); err != nil {
		return nil, err
	}
	if err := tarBody(archive, appbuild.ContainerRuntimePath, runtime, 0o755); err != nil {
		return nil, err
	}
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(appbuild.ContainerLivePath, "/") + "/",
		Mode:     0o1777,
	}); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
}

func RuntimeTag(digest string, runtime []byte) string {
	sum := sha256.Sum256(runtime)
	return naming.DigestTag(digest) + naming.WordSeparator + "ocel" + naming.WordSeparator + hex.EncodeToString(sum[:])[:runtimeTagHexLen]
}

type Wrapped func(ctx context.Context) (v1.Image, func(), error)

func BuiltArchitecture(ctx context.Context, repository, digest string) (string, error) {
	host, err := DockerHostFromEnv()
	if err != nil {
		return "", err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Architecture(ctx, &http.Client{Transport: transport}, repository+":"+naming.DigestTag(digest))
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
