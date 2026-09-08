package providerkit_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

var (
	nodeRuntime = providerkit.Runtime{Name: providerkit.RuntimeNode, Arch: "x86_64"}
	goRuntime   = providerkit.Runtime{Name: providerkit.RuntimeGo, Arch: "x86_64"}
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
	return runtimeConfig(t, providerkit.RuntimeNode, command)
}

func runtimeConfig(t *testing.T, runtime string, command []string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"runtime": map[string]string{"name": runtime, "arch": "x86_64"},
		"handler": "index.mjs",
		"command": command,
		"id":      "server",
		"app":     "web",
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

func TestFunctionImageCarriesTheStagedTreeUnderTheRootTheRuntimeRunsFrom(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":    "export const handler = () => {}",
		"lib/deep.mjs": "export const deep = 1",
		"config.json":  functionConfig(t, nil),
	})

	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Fatalf("FunctionImage() appended %d layers, want the one carrying the function's tree", len(layers))
	}
	held := tarNames(t, layers[0])
	want := []string{"ocel/app/config.json", "ocel/app/index.mjs", "ocel/app/lib/deep.mjs"}
	if !slices.Equal(held, want) {
		t.Errorf("the layer holds %v, want the staged tree under %s: %v", held, providerkit.FunctionImageRoot, want)
	}
}

func TestFunctionImageBootsANodeFunctionThroughTheMembrane(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export const handler = () => {}",
		"config.json": functionConfig(t, nil),
	})

	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	if !slices.Contains(config.Cmd, providerkit.NodeMembranePath) {
		t.Errorf("the image runs %v, want the membrane a node function's handler is served through", config.Cmd)
	}
}

func TestFunctionImageRefusesAFunctionThatNamesNoCommandAndBootsThroughNoMembrane(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"server":      "a built binary",
		"config.json": runtimeConfig(t, providerkit.RuntimeGo, nil),
	})

	_, err := providerkit.FunctionImage(empty.Image, goRuntime, dir, nil)
	if err == nil {
		t.Fatal("FunctionImage() built an image for a go function with no command, want it refused: the image would boot node over a binary that is never run")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("FunctionImage() = %v, want it to name the command the artifact carries none of", err)
	}
}

func TestFunctionImageRunsWhatTheRuntimeItIsBuiltAgainstNames(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"server":      "a built binary",
		"config.json": runtimeConfig(t, providerkit.RuntimeNode, nil),
	})

	_, err := providerkit.FunctionImage(empty.Image, goRuntime, dir, nil)
	if err == nil {
		t.Fatal("FunctionImage() built a go image booting the node membrane because the staged config said node, want the runtime the base was chosen for to decide")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("FunctionImage() = %v, want it to name the command the artifact carries none of", err)
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

	image, err := providerkit.FunctionImage(base, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	port := providerkit.InjectedPortName + "=" + providerkit.InjectedPortText
	if !slices.Contains(config.Env, port) {
		t.Errorf("the image carries env %v, want %s: the function is reached on the port ocel binds it to", config.Env, port)
	}
	if slices.Contains(config.Env, "PORT=3000") {
		t.Errorf("the image carries env %v, want the base's own port replaced rather than left to win", config.Env)
	}
	if !slices.Contains(config.Env, "PATH=/usr/bin") {
		t.Errorf("the image carries env %v, want the base's own entries kept", config.Env)
	}
}

func builtDigest(t *testing.T, files map[string]string) string {
	t.Helper()
	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, stagedFunc(t, files), nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest.String()
}

func TestFunctionImageOfTheSameFunctionCarriesTheSameDigest(t *testing.T) {
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

	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	if !slices.Equal(config.Cmd, []string{"./server"}) {
		t.Errorf("the image runs %v, want the command the function's config names", config.Cmd)
	}
	if config.WorkingDir != providerkit.FunctionImageRoot {
		t.Errorf("the image runs from %q, want %q, where the command's relative path resolves", config.WorkingDir, providerkit.FunctionImageRoot)
	}
}

func TestFunctionImageCarriesTheMembraneAtThePathItBootsFrom(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})
	membrane := []byte("export const membrane = 1")

	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, dir,
		map[string][]byte{providerkit.NodeMembranePath: membrane})
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	held := tarNames(t, layers[0])
	want := strings.TrimPrefix(providerkit.NodeMembranePath, "/")
	if !slices.Contains(held, want) {
		t.Fatalf("the layer holds %v and nothing at %s, so the image runs a membrane it does not carry", held, providerkit.NodeMembranePath)
	}
	if body := tarBody(t, layers[0], want); !bytes.Equal(body, membrane) {
		t.Errorf("the image carries %q at %s, want the membrane it was handed", body, providerkit.NodeMembranePath)
	}
}

func TestFunctionImageTellsTheMembraneWhichHandlerToServe(t *testing.T) {
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export default () => {}",
		"config.json": functionConfig(t, nil),
	})

	image, err := providerkit.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatalf("FunctionImage() error = %v", err)
	}

	config := configOf(t, image)
	want := "OCEL_HANDLER=" + providerkit.FunctionImageRoot + "/index.mjs"
	if !slices.Contains(config.Env, want) {
		t.Errorf("the image carries env %v and never %s, so the membrane has no handler to serve", config.Env, want)
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
		return held
	}
}
