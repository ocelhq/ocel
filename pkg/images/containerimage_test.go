package images_test

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

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/images"
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
		t.Fatal("the wrapped image has no layer at all")
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
			t.Fatalf("the layer contains nothing at %s", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != name {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return header, body
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

	wrapped, err := images.WrapContainer(base, []byte("a runtime"), nil)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	config := configOf(t, wrapped)
	if !slices.Equal(config.Entrypoint, []string{containerimage.RuntimePath}) {
		t.Errorf("the wrapped image enters at %v, want the runtime alone: it is what the container boots through", config.Entrypoint)
	}
	if want := []string{"/bin/sh", "-c", "node server.js"}; !slices.Equal(config.Cmd, want) {
		t.Errorf("the wrapped image runs %v, want %v: the base's own entrypoint and command are what the runtime executes", config.Cmd, want)
	}
	if !slices.Equal(config.Env, []string{"PATH=/usr/bin"}) || config.WorkingDir != "/srv" || config.User != "app" {
		t.Errorf("the wrapped image has env %v, working dir %q and user %q, want the base's own untouched", config.Env, config.WorkingDir, config.User)
	}
}

func TestWrapContainerIncludesTheRuntimeExecutableAtThePathItBootsFrom(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"node", "server.js"}})
	runtime := []byte("a runtime binary")

	wrapped, err := images.WrapContainer(base, runtime, nil)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	name := strings.TrimPrefix(containerimage.RuntimePath, "/")
	header, body := tarEntry(t, lastLayer(t, wrapped), name)
	if !bytes.Equal(body, runtime) {
		t.Errorf("the layer contains %q at %s, want the runtime it was handed", body, containerimage.RuntimePath)
	}
	if header.Mode&0o111 == 0 || header.Mode != 0o755 {
		t.Errorf("the layer has %s at mode %o, want 0755: the entrypoint the container boots through must be executable", containerimage.RuntimePath, header.Mode)
	}
}

func TestWrapContainerIncludesALiveDirectoryAnyImageUserCanProjectInto(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"node", "server.js"}, User: "1000"})
	wrapped, err := images.WrapContainer(base, []byte("a runtime"), nil)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	name := strings.TrimPrefix(containerimage.LivePath, "/") + "/"
	header, _ := tarEntry(t, lastLayer(t, wrapped), name)
	if header.Typeflag != tar.TypeDir {
		t.Fatalf("the layer has %s as type %q, want a directory the runtime projects live values into", containerimage.LivePath, header.Typeflag)
	}
	if header.Mode != 0o1777 {
		t.Errorf("the layer has %s at mode %o, want 1777: an image that runs as its own user, or from scratch with no /tmp, still needs somewhere the runtime can write", containerimage.LivePath, header.Mode)
	}
}

func TestWrapContainerRefusesAnImageThereIsNothingToRunInFrontOf(t *testing.T) {
	t.Parallel()

	_, err := images.WrapContainer(empty.Image, []byte("a runtime"), nil)
	if err == nil {
		t.Fatal("WrapContainer() wrapped an image naming neither an entrypoint nor a command, and the container would boot the runtime over nothing")
	}
	for _, want := range []string{"ENTRYPOINT", "CMD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WrapContainer() = %v, want it to name %s as what the image lacks", err, want)
		}
	}
}

func wrappedDigestOf(t *testing.T, runtime []byte) string {
	t.Helper()
	base := baseContainer(t, v1.Config{Entrypoint: []string{"/app/server"}})
	wrapped, err := images.WrapContainer(base, runtime, nil)
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}
	digest, err := wrapped.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.String()
}

func TestWrappingOneImageInOneRuntimeTwiceGivesTheSameDigest(t *testing.T) {
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

	tag := images.RuntimeTag(wrappedDigest, []byte("a runtime binary"), nil)
	prefix := "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-ocel-"
	if !strings.HasPrefix(tag, prefix) {
		t.Fatalf("RuntimeTag() = %q, want it to open with %q: the tag is read back as the image the deploy built", tag, prefix)
	}
	if digest := strings.TrimPrefix(tag, prefix); len(digest) != 12 {
		t.Errorf("RuntimeTag() names the runtime as %q, want twelve hex characters", digest)
	}
	if other := images.RuntimeTag(wrappedDigest, []byte("a newer runtime binary"), nil); other == tag {
		t.Error("two runtimes share one tag, so a rebuilt runtime would be read as already pushed and never reach the registry")
	}
}

func TestImagesWrappedUnderDifferentRuntimeTagsNeverShareAManifest(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"node", "server.js"}})
	manifests := map[string]string{}
	for _, runtime := range [][]byte{[]byte("a runtime binary"), []byte("a newer runtime binary")} {
		for _, next := range []*images.NextServerRuntime{nil, nextServerRuntime(nextFiles())} {
			wrapped, err := images.WrapContainer(base, runtime, next)
			if err != nil {
				t.Fatal(err)
			}
			manifests[images.RuntimeTag(wrappedDigest, runtime, next)] = digestOf(t, wrapped)
		}
	}

	distinct := map[string]bool{}
	for _, manifest := range manifests {
		distinct[manifest] = true
	}
	if len(manifests) != 4 || len(distinct) != len(manifests) {
		t.Errorf("the runtime tags name the manifests %v, want every tag to name its own: a registry removes a manifest with every tag on it, so two tags on one manifest would let one release's removal take the other's image", manifests)
	}
}

func TestImagesWrappedFromBasesThatSplitTheirCommandDifferentlyNeverShareAManifest(t *testing.T) {
	t.Parallel()

	runtime := []byte("a runtime binary")
	manifests := map[string]string{}
	for _, config := range []v1.Config{
		{Entrypoint: []string{"node"}, Cmd: []string{"server.js"}},
		{Cmd: []string{"node", "server.js"}},
		{Entrypoint: []string{"node", "server.js"}},
	} {
		base := baseContainer(t, config)
		wrapped, err := images.WrapContainer(base, runtime, nil)
		if err != nil {
			t.Fatal(err)
		}
		manifests[images.RuntimeTag(digestOf(t, base), runtime, nil)] = digestOf(t, wrapped)
	}

	distinct := map[string]bool{}
	for _, manifest := range manifests {
		distinct[manifest] = true
	}
	if len(manifests) != 3 || len(distinct) != len(manifests) {
		t.Errorf("the runtime tags name the manifests %v, want every tag to name its own: the wrap folds ENTRYPOINT and CMD into one command, and a removal that falls back to deleting by digest would take every tag on a shared manifest", manifests)
	}
}

func nextServerRuntime(files map[string][]byte) *images.NextServerRuntime {
	return &images.NextServerRuntime{Files: files}
}

func nextFiles() map[string][]byte {
	return map[string][]byte{
		containerimage.NextServerPreloadFile: []byte("a preload"),
		"server-chunk.mjs":                   []byte("a server chunk"),
	}
}

func digestOf(t *testing.T, image v1.Image) string {
	t.Helper()
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.String()
}

func TestAWrappedNextContainerShipsTheServerPreloadInTheNextRuntimeDirectory(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"next", "start"}})
	wrapped, err := images.WrapContainer(base, []byte("a runtime"), nextServerRuntime(nextFiles()))
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	layer := lastLayer(t, wrapped)
	if header, _ := tarEntry(t, layer, "ocel/runtime/next/"); header.Typeflag != tar.TypeDir || header.Mode != 0o755 {
		t.Errorf("the layer has the Next runtime directory as type %q mode %o, want a 0755 directory", header.Typeflag, header.Mode)
	}
	for name, want := range nextFiles() {
		header, body := tarEntry(t, layer, "ocel/runtime/next/"+name)
		if !bytes.Equal(body, want) {
			t.Errorf("the layer holds %q at /ocel/runtime/next/%s, want %q", body, name, want)
		}
		if header.Mode != 0o644 {
			t.Errorf("/ocel/runtime/next/%s is mode %o, want 0644", name, header.Mode)
		}
	}
	if config := configOf(t, wrapped); !slices.Equal(config.Entrypoint, []string{containerimage.RuntimePath}) || !slices.Equal(config.Cmd, []string{"next", "start"}) {
		t.Errorf("the wrapped image enters at %v and runs %v, want the runtime in front of the image's own command", config.Entrypoint, config.Cmd)
	}
}

func TestAWrappedNextContainerKeepsTheEnvironmentItsImageSet(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"next", "start"}, Env: []string{"PATH=/usr/bin", "NODE_OPTIONS=--max-old-space-size=512"}})
	wrapped, err := images.WrapContainer(base, []byte("a runtime"), nextServerRuntime(nextFiles()))
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	want := []string{"PATH=/usr/bin", "NODE_OPTIONS=--max-old-space-size=512"}
	if got := configOf(t, wrapped).Env; !slices.Equal(got, want) {
		t.Errorf("the wrapped image has env %v, want %v, with the preload left to the container runtime", got, want)
	}
}

func TestWrapContainerLeavesAnImageThatServesNoNextAsItWas(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Entrypoint: []string{"/app/server"}})
	plain, err := images.WrapContainer(base, []byte("a runtime"), nil)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := plain.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Errorf("a wrap with no Next server runtime has %d layers, want the runtime's alone", len(layers))
	}
	if env := configOf(t, plain).Env; len(env) != 0 {
		t.Errorf("a wrap with no Next server runtime sets env %v, want none", env)
	}
	if got, want := digestOf(t, plain), wrappedDigestOf(t, []byte("a runtime")); got != want {
		t.Errorf("a nil Next server runtime digests %s, want today's %s", got, want)
	}
}

func TestAWrappedNextContainerLeavesNextAdapterPathToTheApp(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"next", "start"}, Env: []string{"NEXT_ADAPTER_PATH=/app/mine.js"}})
	wrapped, err := images.WrapContainer(base, []byte("a runtime"), nextServerRuntime(nextFiles()))
	if err != nil {
		t.Fatalf("WrapContainer() = %v", err)
	}

	want := []string{"NEXT_ADAPTER_PATH=/app/mine.js"}
	if got := configOf(t, wrapped).Env; !slices.Equal(got, want) {
		t.Errorf("the wrapped image has env %v, want %v", got, want)
	}
}

func TestWrapContainerRefusesANextServerRuntimeWithoutItsPreload(t *testing.T) {
	t.Parallel()

	base := baseContainer(t, v1.Config{Cmd: []string{"next", "start"}})
	_, err := images.WrapContainer(base, []byte("a runtime"), nextServerRuntime(map[string][]byte{"server-chunk.mjs": []byte("x")}))
	if err == nil || !strings.Contains(err.Error(), containerimage.NextServerPreloadFile) {
		t.Errorf("WrapContainer() = %v, want a refusal naming %s", err, containerimage.NextServerPreloadFile)
	}
}

func TestWrappingOneNextImageTwiceGivesTheSameDigest(t *testing.T) {
	t.Parallel()

	digests := make([]string, 2)
	for i := range digests {
		base := baseContainer(t, v1.Config{Cmd: []string{"next", "start"}})
		wrapped, err := images.WrapContainer(base, []byte("a runtime"), nextServerRuntime(nextFiles()))
		if err != nil {
			t.Fatal(err)
		}
		digests[i] = digestOf(t, wrapped)
	}
	if digests[0] != digests[1] {
		t.Errorf("two wraps of one Next image digest %s and %s, want one", digests[0], digests[1])
	}
}

func TestTheRuntimeTagChangesWhenTheNextServerRuntimeDoes(t *testing.T) {
	t.Parallel()

	runtime := []byte("a runtime binary")
	tag := images.RuntimeTag(wrappedDigest, runtime, nextServerRuntime(nextFiles()))
	changedFile := nextFiles()
	changedFile["server-chunk.mjs"] = []byte("a newer server chunk")
	addedFile := nextFiles()
	addedFile["extra.cjs"] = []byte("x")
	for name, other := range map[string]*images.NextServerRuntime{
		"none":           nil,
		"a changed file": nextServerRuntime(changedFile),
		"an added file":  nextServerRuntime(addedFile),
	} {
		if images.RuntimeTag(wrappedDigest, runtime, other) == tag {
			t.Errorf("a Next server runtime with %s tags like the one it replaces, so the registry would keep serving the old one", name)
		}
	}
	if again := images.RuntimeTag(wrappedDigest, runtime, nextServerRuntime(nextFiles())); again != tag {
		t.Errorf("one Next server runtime tags %s and %s", tag, again)
	}
}
