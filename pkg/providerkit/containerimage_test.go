package providerkit_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const wrappedDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func baseContainer(t *testing.T, config v1.Config) v1.Image {
	t.Helper()
	base, err := mutate.Config(empty.Image, config)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func lastLayer(t *testing.T, image v1.Image) v1.Layer {
	t.Helper()
	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) == 0 {
		t.Fatal("the wrapped image carries no layer at all")
	}
	return layers[len(layers)-1]
}

func tarEntry(t *testing.T, layer v1.Layer, name string) (*tar.Header, []byte) {
	t.Helper()
	body, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	reader := tar.NewReader(body)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("the layer holds nothing at %s", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != name {
			continue
		}
		held, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return header, held
	}
}

func TestWrapContainerBootsTheImagesOwnCommandThroughTheRuntime(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"node server.js"},
		Env:        []string{"PATH=/usr/bin"},
		WorkingDir: "/srv",
		User:       "app",
	})

	wrapped, err := providerkit.WrapContainer(base, []byte("a runtime"))
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	config := configOf(t, wrapped)
	if !slices.Equal(config.Entrypoint, []string{providerkit.ContainerRuntimePath}) {
		t.Errorf("the wrapped image enters at %v, want the runtime alone: it is what the container boots through", config.Entrypoint)
	}
	if want := []string{"/bin/sh", "-c", "node server.js"}; !slices.Equal(config.Cmd, want) {
		t.Errorf("the wrapped image runs %v, want %v: the base's own entrypoint and command are what the runtime executes", config.Cmd, want)
	}
	if !slices.Equal(config.Env, []string{"PATH=/usr/bin"}) || config.WorkingDir != "/srv" || config.User != "app" {
		t.Errorf("the wrapped image carries env %v, working dir %q and user %q, want the base's own untouched", config.Env, config.WorkingDir, config.User)
	}
}

func TestWrapContainerCarriesTheRuntimeExecutableAtThePathItBootsFrom(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"node", "server.js"}})
	runtime := []byte("a runtime binary")

	wrapped, err := providerkit.WrapContainer(base, runtime)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	name := strings.TrimPrefix(providerkit.ContainerRuntimePath, "/")
	header, held := tarEntry(t, lastLayer(t, wrapped), name)
	if !bytes.Equal(held, runtime) {
		t.Errorf("the layer holds %q at %s, want the runtime it was handed", held, providerkit.ContainerRuntimePath)
	}
	if header.Mode&0o111 == 0 || header.Mode != 0o755 {
		t.Errorf("the layer holds %s at mode %o, want 0755: the entrypoint the container boots through must be executable", providerkit.ContainerRuntimePath, header.Mode)
	}
}

func TestWrapContainerCarriesALiveDirectoryAnyImageUserCanProjectInto(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"node", "server.js"}, User: "1000"})
	wrapped, err := providerkit.WrapContainer(base, []byte("a runtime"))
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	name := strings.TrimPrefix(providerkit.ContainerLivePath, "/") + "/"
	header, _ := tarEntry(t, lastLayer(t, wrapped), name)
	if header.Typeflag != tar.TypeDir {
		t.Fatalf("the layer holds %s as type %q, want a directory the runtime projects live values into", providerkit.ContainerLivePath, header.Typeflag)
	}
	if header.Mode != 0o1777 {
		t.Errorf("the layer holds %s at mode %o, want 1777: an image that runs as its own user, or from scratch with no /tmp, still needs somewhere the runtime can write", providerkit.ContainerLivePath, header.Mode)
	}
}

func TestWrapContainerRefusesAnImageThereIsNothingToRunInFrontOf(t *testing.T) {
	t.Parallel()

	_, err := providerkit.WrapContainer(empty.Image, []byte("a runtime"))
	if err == nil {
		t.Fatal("WrapContainer() wrapped an image naming neither an entrypoint nor a command, and the container would boot the runtime over nothing")
	}
	for _, want := range []string{"ENTRYPOINT", "CMD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WrapContainer() = %v, want it to name %s as what the image carries none of", err, want)
		}
	}
}

func wrappedDigestOf(t *testing.T, runtime []byte) string {
	t.Helper()
	base := baseContainer(t, v1.Config{Entrypoint: []string{"/app/server"}})
	wrapped, err := providerkit.WrapContainer(base, runtime)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}
	digest, err := wrapped.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.String()
}

func TestWrappingOneImageInOneRuntimeTwiceCarriesTheSameDigest(t *testing.T) {
	t.Parallel()

	runtime := []byte("a runtime binary")
	first, second := wrappedDigestOf(t, runtime), wrappedDigestOf(t, runtime)
	if first != second {
		t.Errorf("two wraps of one image digest %s and %s, want one: a redeploy of the same image and runtime pushes nothing new", first, second)
	}
	if changed := wrappedDigestOf(t, []byte("a newer runtime binary")); changed == first {
		t.Error("an image wrapped in a newer runtime digests the same as the one it replaces, so the release would keep running the runtime it was meant to replace")
	}
}

func TestTheRuntimeTagNamesTheImagesDigestAndTheRuntimeItIsWrappedIn(t *testing.T) {
	t.Parallel()

	tag := providerkit.RuntimeTag(wrappedDigest, []byte("a runtime binary"))
	prefix := "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-ocel-"
	if !strings.HasPrefix(tag, prefix) {
		t.Fatalf("RuntimeTag() = %q, want it to open with %q: the tag is read back as the image the deploy built", tag, prefix)
	}
	if held := strings.TrimPrefix(tag, prefix); len(held) != 12 {
		t.Errorf("RuntimeTag() names the runtime as %q, want twelve hex characters", held)
	}
	if other := providerkit.RuntimeTag(wrappedDigest, []byte("a newer runtime binary")); other == tag {
		t.Error("two runtimes share one tag, so a rebuilt runtime would be read as already pushed and never reach the registry")
	}
}
