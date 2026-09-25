package providerkit

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
)

const (
	ContainerRuntimePath = "/ocel/bin/runtime"
	ContainerLivePath    = "/ocel/live"
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
		return nil, Refuse(CodeInvalid,
			"the image names neither an ENTRYPOINT nor a CMD, so there is nothing for the runtime to run in front of: give it one")
	}
	if len(runtime) == 0 {
		return nil, Refuse(CodeNotReady, "this provider carries no runtime built for %s", file.Architecture)
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
	config.Entrypoint = []string{ContainerRuntimePath}
	config.Cmd = command
	return mutate.Config(appended, config)
}

func runtimeLayer(runtime []byte) ([]byte, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(ContainerRuntimePath[:strings.LastIndex(ContainerRuntimePath, "/")], "/") + "/",
		Mode:     0o755,
	}); err != nil {
		return nil, err
	}
	if err := tarBody(archive, ContainerRuntimePath, runtime, 0o755); err != nil {
		return nil, err
	}
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(ContainerLivePath, "/") + "/",
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

func (r *deployRun) wrappedPush(ctx context.Context, entry AppEntry) (ImagePush, error) {
	runtimePort := r.provider.Runtime()
	app, ref := entry.App, entry.Image
	repository, digest, pinned := strings.Cut(ref, "@")
	if !pinned || repository == "" || digest == "" {
		return ImagePush{}, Refuse(CodeInvalid,
			"app %s carries the image %q, which pins no digest, so there is nothing to push under a coordinate", app, ref)
	}
	arch, err := builtArchitecture(ctx, repository, digest)
	if err != nil {
		return ImagePush{}, fmt.Errorf("read the architecture %s's image is built for: %w", app, err)
	}
	runs, err := runtimePort.Arch(ctx, app, entry.Arch)
	if err != nil {
		return ImagePush{}, fmt.Errorf("read the architecture %s's container runs on: %w", app, err)
	}
	if arch != runs {
		return ImagePush{}, Refuse(CodeInvalid,
			"app %s's image is built for %s and the target runs %s, which cannot execute it: build it for %s, and drop any --platform its Dockerfile pins a FROM to",
			app, ContainerPlatform(arch), ContainerPlatform(runs), ContainerPlatform(runs))
	}
	runtime, err := runtimePort.Binary(ctx, arch)
	if err != nil {
		return ImagePush{}, fmt.Errorf("read the runtime %s's container boots through: %w", app, err)
	}
	if len(runtime) == 0 {
		return ImagePush{}, Refuse(CodeNotReady,
			"this provider carries no container runtime built for %s, and %s's image is built for it", arch, app)
	}
	return ImagePush{
		App:      app,
		Source:   ref,
		ImageRef: imageRef(repository, RuntimeTag(digest, runtime), r.registry),
		Wrap: func(ctx context.Context) (v1.Image, func(), error) {
			return wrapFromDaemon(ctx, repository, digest, runtime)
		},
	}, nil
}

func builtArchitecture(ctx context.Context, repository, digest string) (string, error) {
	host, err := DockerHostFromEnv()
	if err != nil {
		return "", err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Architecture(ctx, &http.Client{Transport: transport}, repository+":"+naming.DigestTag(digest))
}

func wrapFromDaemon(ctx context.Context, repository, digest string, runtime []byte) (v1.Image, func(), error) {
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
	checked := CompleteArchive(stream, host.Address, ref)
	_, copyErr := io.Copy(saved, checked)
	closeErr := saved.Close()
	if gap := checked.Gap(); gap != nil {
		discard()
		return nil, nil, gap
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
