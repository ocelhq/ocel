package build

import (
	"context"
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/provider"
)

func workerFixture(t *testing.T, name string) *project.Project {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "worker", name))
	if err != nil {
		t.Fatal(err)
	}
	return &project.Project{Slug: "worker-" + name, Dir: dir, Apps: []project.App{{Name: "web", Path: ".", Compute: "container"}}}
}

type addedFiles struct {
	base    image.Image
	dst     string
	arch    string
	entries map[string][]byte
}

func buildingWithWorkers(t *testing.T, cfg *project.Project, workers HostedWorkers) (Output, *addedFiles, error) {
	t.Helper()
	var added *addedFiles
	built, err := tools{
		image: func(_ context.Context, app image.App, _ string, _ image.LiveValues, _ io.Writer) (image.Image, error) {
			return image.Image{Repository: "ocel/" + cfg.Slug + "/" + app.Name, Tag: "sha256-" + strings.Repeat("a", 64), Ref: "ocel/" + cfg.Slug + "/" + app.Name + "@sha256:" + strings.Repeat("a", 64)}, nil
		},
		inspection: daemonHolding("arm64"),
		addFiles: func(_ context.Context, base image.Image, _, _, files, dst, arch string, _ io.Writer) (image.Image, error) {
			added = &addedFiles{base: base, dst: dst, arch: arch, entries: map[string][]byte{}}
			found, err := os.ReadDir(files)
			if err != nil {
				return image.Image{}, err
			}
			for _, entry := range found {
				read, err := os.ReadFile(filepath.Join(files, entry.Name()))
				if err != nil {
					return image.Image{}, err
				}
				added.entries[entry.Name()] = read
			}
			return image.Image{Ref: base.Repository + "@sha256:" + strings.Repeat("b", 64)}, nil
		},
	}.apps(context.Background(), cfg, nil, map[string]string{"web": ""}, workers, Host{}, Log{})
	return built, added, err
}

func TestAContainerAppHostingANodeWorkerIsBuiltIntoAnImageCarryingTheWorkerEntry(t *testing.T) {
	cfg := workerFixture(t, "node")
	built, added, err := buildingWithWorkers(t, cfg, HostedWorkers{"web": {discovery.DefaultRootDirName + "/index.ts:1"}})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
	if added == nil {
		t.Fatal("the image gained no worker entry, so every worker container would run the app instead")
	}
	if added.dst != containerimage.WorkerDir || len(added.entries[containerimage.NodeWorkerEntry.ImageFile]) == 0 {
		t.Errorf("the image gained %v at %s, want %s at %s", keysOf(added.entries), added.dst, containerimage.NodeWorkerEntry.ImageFile, containerimage.WorkerDir)
	}
	if want := "ocel/worker-node/web@sha256:" + strings.Repeat("b", 64); built.Images["web"] != want {
		t.Errorf("the build reported %q for web, want the image that carries the worker entry", built.Images["web"])
	}
}

func TestAContainerAppHostingAGoWorkerCarriesTheWorkerBinaryBuiltForItsImagesArchitecture(t *testing.T) {
	cfg := workerFixture(t, "go")
	_, added, err := buildingWithWorkers(t, cfg, HostedWorkers{"web": {discovery.DefaultRootDirName + "/infra.go:1"}})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
	if added == nil {
		t.Fatal("the image gained no worker entry")
	}
	binary := added.entries[buildoutput.GoWorkerBinary]
	if len(binary) == 0 {
		t.Fatalf("the image gained %v, want %s", keysOf(added.entries), buildoutput.GoWorkerBinary)
	}
	parsed, err := elf.NewFile(strings.NewReader(string(binary)))
	if err != nil {
		t.Fatalf("%s is no linux binary: %v", buildoutput.GoWorkerBinary, err)
	}
	if parsed.Machine != elf.EM_AARCH64 {
		t.Errorf("%s is built for %v, want arm64, the architecture the image was built for", buildoutput.GoWorkerBinary, parsed.Machine)
	}
}

func TestAContainerAppHostingNoWorkerGainsNothing(t *testing.T) {
	cfg := workerFixture(t, "node")
	built, added, err := buildingWithWorkers(t, cfg, nil)
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
	if added != nil {
		t.Errorf("an app hosting no worker gained %v", keysOf(added.entries))
	}
	if built.Images["web"] != "ocel/worker-node/web@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("the build reported %q, want the image as it was built", built.Images["web"])
	}
}

func TestAContainerAppHostingAPythonWorkerIsBuiltIntoAnImageCarryingTheWorkerEntry(t *testing.T) {
	cfg := workerFixture(t, "python")
	_, added, err := buildingWithWorkers(t, cfg, HostedWorkers{"web": {discovery.DefaultRootDirName + "/__init__.py:1"}})
	if err != nil {
		t.Fatalf("apps() = %v, want the python worker built into the image", err)
	}
	if added == nil || added.dst != containerimage.WorkerDir {
		t.Fatalf("the image gained files at %+v, want %s", added, containerimage.WorkerDir)
	}
	entry := string(added.entries[containerimage.PythonWorkerEntry.ImageFile])
	if !strings.Contains(entry, `import_module("`+discovery.DefaultRootDirName+`")`) || strings.Contains(entry, cfg.Dir) {
		t.Errorf("the image gained %v with %q, want an entry importing the discovery folder and naming no build host path", keysOf(added.entries), entry)
	}
}

func keysOf(entries map[string][]byte) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}

func TestAScriptBuiltAppGainsTheWorkerEntryInTheFunctionAWorkerRunsFrom(t *testing.T) {
	cfg := workerFixture(t, "node")
	app := project.App{Name: "web", Path: ".", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}}
	outputDir := t.TempDir()
	appDir := buildoutput.AppRoot(outputDir, app.Name)
	for _, id := range []string{"bundle-1", "bundle-0"} {
		function := filepath.Join(appDir, buildoutput.FunctionsDir, id+buildoutput.FunctionDirSuffix)
		if err := os.MkdirAll(function, 0o755); err != nil {
			t.Fatal(err)
		}
		config := `{"framework":{"name":"next"},"entryFile":"launcher.cjs","id":"` + id + `","app":"web"}`
		if err := os.WriteFile(filepath.Join(function, buildoutput.FunctionConfigFile), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := addWorkerEntryToFunction(context.Background(), cfg, app, outputDir); err != nil {
		t.Fatalf("addWorkerEntryToFunction() = %v", err)
	}

	first := filepath.Join(appDir, buildoutput.FunctionsDir, "bundle-0"+buildoutput.FunctionDirSuffix)
	if _, err := os.Stat(filepath.Join(first, containerimage.NodeWorkerEntry.ArtifactFile)); err != nil {
		t.Errorf("the first function carries no worker entry: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(first, buildoutput.FunctionConfigFile))
	if err != nil || !strings.Contains(string(raw), `"worker": [`) {
		t.Errorf("the first function's config = %s (%v), want it to name the worker command", raw, err)
	}
	other := filepath.Join(appDir, buildoutput.FunctionsDir, "bundle-1"+buildoutput.FunctionDirSuffix)
	if _, err := os.Stat(filepath.Join(other, containerimage.NodeWorkerEntry.ArtifactFile)); err == nil {
		t.Error("a function a worker never runs from carries the worker entry")
	}
}
