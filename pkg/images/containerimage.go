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
	"path"
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
)

type NextServerRuntime struct {
	Dir   string
	Files map[string][]byte
}

func ContainerPlatform(arch string) string { return "linux/" + arch }

func WrapContainer(base v1.Image, runtime []byte, next *NextServerRuntime) (v1.Image, error) {
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
	if next != nil {
		if err := checkNextServerRuntime(next, config.Env); err != nil {
			return nil, err
		}
	}
	packed, err := packRuntimeLayer(runtime)
	if err != nil {
		return nil, err
	}
	addenda := []mutate.Addendum{}
	layer, err := newBytesLayer(packed)
	if err != nil {
		return nil, err
	}
	addenda = append(addenda, mutate.Addendum{Layer: layer})
	if next != nil {
		nextPacked, err := packNextServerLayer(next)
		if err != nil {
			return nil, err
		}
		nextLayer, err := newBytesLayer(nextPacked)
		if err != nil {
			return nil, err
		}
		addenda = append(addenda, mutate.Addendum{Layer: nextLayer})
		config.Env = append(append([]string{}, config.Env...), containerimage.NextAdapterPathVar+"="+path.Join(next.Dir, containerimage.NextServerAdapterFile))
	}
	appended, err := mutate.Append(base, addenda...)
	if err != nil {
		return nil, err
	}
	config.Entrypoint = []string{containerimage.RuntimePath}
	config.Cmd = command
	return mutate.Config(appended, config)
}

func newBytesLayer(packed []byte) (v1.Layer, error) {
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed)), nil
	})
}

func checkNextServerRuntime(next *NextServerRuntime, env []string) error {
	if !path.IsAbs(next.Dir) {
		return refusal.Refuse(refusal.CodeInvalid,
			"the provider names %q as the directory of its Next server runtime, and NEXT_ADAPTER_PATH only finds the adapter at an absolute path", next.Dir)
	}
	if _, ok := next.Files[containerimage.NextServerAdapterFile]; !ok {
		return refusal.Refuse(refusal.CodeInvalid,
			"the provider's Next server runtime holds no %s, so next start has no adapter to load", containerimage.NextServerAdapterFile)
	}
	for _, entry := range env {
		if name, value, _ := strings.Cut(entry, "="); name == containerimage.NextAdapterPathVar {
			return refusal.Refuse(refusal.CodeInvalid,
				"the image sets %s=%s and ocel sets it to load the cache handlers its provider ships: remove it from the image",
				containerimage.NextAdapterPathVar, value)
		}
	}
	return nil
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func packNextServerLayer(next *NextServerRuntime) ([]byte, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(next.Dir, "/") + "/",
		Mode:     0o755,
	}); err != nil {
		return nil, err
	}
	for _, name := range sortedNames(next.Files) {
		if err := tarBody(archive, path.Join(next.Dir, name), next.Files[name], 0o644); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
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

func RuntimeTag(digest string, runtime []byte, next *NextServerRuntime) string {
	hash := sha256.New()
	hash.Write(runtime)
	if next != nil {
		hash.Write([]byte{0})
		hash.Write([]byte(next.Dir))
		for _, name := range sortedNames(next.Files) {
			body := next.Files[name]
			fmt.Fprintf(hash, "\x00%d:%s\x00%d:", len(name), name, len(body))
			hash.Write(body)
		}
	}
	return naming.DigestTag(digest) + naming.WordSeparator + "ocel" + naming.WordSeparator + hex.EncodeToString(hash.Sum(nil))[:runtimeTagHexLen]
}

func BuiltArchitecture(ctx context.Context, repository, digest string) (string, error) {
	host, err := DockerHostFromEnv()
	if err != nil {
		return "", err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Architecture(ctx, &http.Client{Transport: transport}, repository+":"+naming.DigestTag(digest))
}

func WrapFromDaemon(ctx context.Context, repository, digest string, runtime []byte, next *NextServerRuntime) (v1.Image, func(), error) {
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
	wrapped, err := WrapContainer(base, runtime, next)
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
