package images_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
)

var (
	nodeRuntime = appbuild.Framework{Name: appbuild.FrameworkNode, Arch: "x86_64"}
	goRuntime   = appbuild.Framework{Name: appbuild.FrameworkGo, Arch: "x86_64"}
)

func stagedFunc(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func functionConfig(t *testing.T, command []string) string {
	t.Helper()
	return frameworkConfig(t, appbuild.FrameworkNode, command)
}

func frameworkConfig(t *testing.T, framework string, command []string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"framework": map[string]string{"name": framework, "arch": "x86_64"},
		"handler":   "index.mjs",
		"command":   command,
		"id":        "server",
		"app":       "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func tarNames(t *testing.T, layer v1.Layer) []string {
	t.Helper()
	body, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var names []string
	reader := tar.NewReader(body)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, header.Name)
	}
	slices.Sort(names)
	return names
}

func TestFunctionImageIncludesTheStagedTreeUnderTheRootTheRuntimeRunsFrom(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":    "export const handler = () => {}",
		"lib/deep.mjs": "export const deep = 1",
		"config.json":  functionConfig(t, nil),
	})

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Fatalf("FunctionImage() appended %d layers, want the one containing the function's tree", len(layers))
	}
	names := tarNames(t, layers[0])
	want := []string{"ocel/app/config.json", "ocel/app/index.mjs", "ocel/app/lib/deep.mjs"}
	if !slices.Equal(names, want) {
		t.Errorf("the layer contains %v, want the staged tree under %s: %v", names, images.FunctionImageRoot, want)
	}
}

func TestFunctionImageBootsANodeFunctionThroughTheRuntime(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export const handler = () => {}",
		"config.json": functionConfig(t, nil),
	})

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	if !slices.Contains(config.Cmd, images.NodeRuntimePath) {
		t.Errorf("the image runs %v, want the runtime a node function's handler is served through", config.Cmd)
	}
}

func TestFunctionImageRefusesAFunctionThatNamesNoCommandAndBootsThroughNoRuntime(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"server":      "a built binary",
		"config.json": frameworkConfig(t, appbuild.FrameworkGo, nil),
	})

	_, err := images.FunctionImage(empty.Image, goRuntime, dir, nil)
	if err == nil {
		t.Fatal("FunctionImage() built an image for a go function with no command, want it refused: the image would boot node over a binary that is never run")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("FunctionImage() = %v, want it to name the command the artifact lacks", err)
	}
}

func TestFunctionImageRunsWhatTheRuntimeItIsBuiltAgainstNames(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"server":      "a built binary",
		"config.json": frameworkConfig(t, appbuild.FrameworkNode, nil),
	})

	_, err := images.FunctionImage(empty.Image, goRuntime, dir, nil)
	if err == nil {
		t.Fatal("FunctionImage() built a go image booting the node runtime because the staged config said node, want the runtime the base was chosen for to decide")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("FunctionImage() = %v, want it to name the command the artifact lacks", err)
	}
}

func TestFunctionImageTellsTheFunctionWhichPortToBind(t *testing.T) {
	base, err := mutate.Config(empty.Image, v1.Config{Env: []string{"PATH=/usr/bin", "PORT=3000"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export const handler = () => {}",
		"config.json": functionConfig(t, nil),
	})

	image, err := images.FunctionImage(base, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	port := appbuild.InjectedPortName + "=" + appbuild.InjectedPortText
	if !slices.Contains(config.Env, port) {
		t.Errorf("the image has env %v, want %s: the function is reached on the port ocel binds it to", config.Env, port)
	}
	if slices.Contains(config.Env, "PORT=3000") {
		t.Errorf("the image has env %v, want the base's own port replaced rather than left to win", config.Env)
	}
	if !slices.Contains(config.Env, "PATH=/usr/bin") {
		t.Errorf("the image has env %v, want the base's own entries kept", config.Env)
	}
}

func builtDigest(t *testing.T, files map[string]string) string {
	t.Helper()
	image, err := images.FunctionImage(empty.Image, nodeRuntime, stagedFunc(t, files), nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.String()
}

func TestFunctionImageOfTheSameFunctionGetsTheSameDigest(t *testing.T) {
	files := map[string]string{
		"index.mjs":   "export const handler = () => {}",
		"config.json": functionConfig(t, nil),
	}

	first, second := builtDigest(t, files), builtDigest(t, files)
	if first != second {
		t.Errorf("two builds of one function digest %s and %s, want one digest: a redeploy of the same tree pushes nothing new", first, second)
	}

	changed := map[string]string{
		"index.mjs":   "export const handler = () => 1",
		"config.json": files["config.json"],
	}
	if builtDigest(t, changed) == first {
		t.Error("a changed function digests the same as the one it replaces, so the release would run the code it was meant to replace")
	}
}

func configOf(t *testing.T, image v1.Image) v1.Config {
	t.Helper()
	file, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	return file.Config
}

func TestFunctionImageRunsTheCommandTheFunctionsConfigNames(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"server":      "a built binary",
		"config.json": functionConfig(t, []string{"./server"}),
	})

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	if !slices.Equal(config.Cmd, []string{"./server"}) {
		t.Errorf("the image runs %v, want the command the function's config names", config.Cmd)
	}
	if config.WorkingDir != images.FunctionImageRoot {
		t.Errorf("the image runs from %q, want %q, where the command's relative path resolves", config.WorkingDir, images.FunctionImageRoot)
	}
}

func TestFunctionImageIncludesTheRuntimeAtThePathItBootsFrom(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})
	runtime := []byte("export const runtime = 1")

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir,
		map[string][]byte{images.NodeRuntimePath: runtime})
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, layers[0])
	want := strings.TrimPrefix(images.NodeRuntimePath, "/")
	if !slices.Contains(names, want) {
		t.Fatalf("the layer contains %v and nothing at %s, so the image runs a runtime it does not include", names, images.NodeRuntimePath)
	}
	if body := tarBody(t, layers[0], want); !bytes.Equal(body, runtime) {
		t.Errorf("the image has %q at %s, want the runtime it was handed", body, images.NodeRuntimePath)
	}
}

func TestFunctionImageTellsTheRuntimeWhichHandlerToServe(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	want := "OCEL_HANDLER=" + images.FunctionImageRoot + "/index.mjs"
	if !slices.Contains(config.Env, want) {
		t.Errorf("the image has env %v and never %s, so the runtime has no handler to serve", config.Env, want)
	}
}

func TestFunctionImageRefusesAnOverlayThatWritesOutsideTheFunctionAndItsRuntime(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})

	for _, rel := range []string{"/etc/passwd", "../../../etc/passwd", "/ocel/runtimes/entrypoint.mjs"} {
		_, err := images.FunctionImage(empty.Image, nodeRuntime, dir,
			map[string][]byte{rel: []byte("root::0:0::/:/bin/sh")})
		if err == nil {
			t.Fatalf("FunctionImage() included an overlay at %s, want it refused: a function's image may not write over the base image it is built on", rel)
		}
		if !strings.Contains(err.Error(), rel) {
			t.Errorf("FunctionImage() = %v, want it to name %s as the path it refuses", err, rel)
		}
	}
}

func TestFunctionImageIncludesAnOverlayAlongsideTheRuntimeItBootsFrom(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})
	beside := path.Join(path.Dir(images.NodeRuntimePath), "lib/shim.mjs")

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir,
		map[string][]byte{beside: []byte("export const shim = 1")})
	if err != nil {
		t.Fatalf("FunctionImage() error = %v, want the runtime's own directory included: it is the one place outside the function's tree the image is built to contain", err)
	}

	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if names := tarNames(t, layers[0]); !slices.Contains(names, strings.TrimPrefix(beside, "/")) {
		t.Errorf("the layer contains %v and nothing at %s", names, beside)
	}
}

func TestTheContainerRuntimeLandsOutsideBothTreesAFunctionImageContains(t *testing.T) {
	t.Parallel()

	landed := appbuild.ContainerRuntimePath
	for _, root := range []string{images.FunctionImageRoot, images.NodeRuntimeRoot} {
		if landed == root || strings.HasPrefix(landed, root+"/") {
			t.Errorf("the container runtime lands at %s, inside %s: a node function's image has a directory there, and a file appended over a directory cannot be loaded", appbuild.ContainerRuntimePath, root)
		}
	}

	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})
	if _, err := images.FunctionImage(empty.Image, nodeRuntime, dir,
		map[string][]byte{appbuild.ContainerRuntimePath: []byte("theirs")}); err == nil {
		t.Errorf("FunctionImage() included an overlay at %s, want it refused: a function's overlay may not write over the runtime its image is later wrapped in", appbuild.ContainerRuntimePath)
	}

	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir,
		map[string][]byte{images.NodeRuntimePath: []byte("export const runtime = 1")})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := images.WrapContainer(image, []byte("a runtime binary"))
	if err != nil {
		t.Fatalf("WrapContainer() = %v, want a node function's image wrapped like any container", err)
	}
	config := configOf(t, wrapped)
	if !slices.Equal(config.Entrypoint, []string{appbuild.ContainerRuntimePath}) || !slices.Equal(config.Cmd, []string{"node", images.NodeRuntimePath}) {
		t.Errorf("the wrapped function enters at %v and runs %v, want the container runtime running the node one", config.Entrypoint, config.Cmd)
	}
}

func tarBody(t *testing.T, layer v1.Layer, name string) []byte {
	t.Helper()
	body, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
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
		return body
	}
}

func TestAFunctionsRouteIsWhatIsLeftOfItsLogicalName(t *testing.T) {
	if got, want := images.FunctionRoute("web", "fn--web--index"), "index"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: the app is already named beside it", got, want)
	}
	if got, want := images.FunctionRoute("web", "fn--web--api-users"), "api-users"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q", got, want)
	}
	if got, want := images.FunctionRoute("web", "web"), "web"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: a name that is no coordinate is left alone", got, want)
	}
	if got, want := images.FunctionRoute("web", "fn--web--API/Users_[id]"), "api-users-id"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: every caller names a resource with it, and no registry, "+
			"bucket or service takes what a route may contain", got, want)
	}
}
