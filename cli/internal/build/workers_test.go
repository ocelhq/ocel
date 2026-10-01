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
)

func workerFixture(t *testing.T, name string) *project.Project {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "worker", name))
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
		image: func(_ context.Context, app image.App, _ string, _ io.Writer) (image.Image, error) {
			return image.Image{Repository: "ocel/" + cfg.Slug + "/" + app.Name, Tag: "sha256-" + strings.Repeat("a", 64), Ref: "ocel/" + cfg.Slug + "/" + app.Name + "@sha256:" + strings.Repeat("a", 64)}, nil
		},
		architecture: daemonHolding("arm64"),
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
	}.apps(context.Background(), cfg, nil, map[string]string{"web": ""}, workers, Log{})
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
	if added.dst != containerimage.WorkerDir || len(added.entries[containerimage.NodeWorkerEntry]) == 0 {
		t.Errorf("the image gained %v at %s, want %s at %s", keysOf(added.entries), added.dst, containerimage.NodeWorkerEntry, containerimage.WorkerDir)
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

func TestAContainerAppHostingAPythonWorkerIsRefusedByName(t *testing.T) {
	cfg := workerFixture(t, "python")
	_, _, err := buildingWithWorkers(t, cfg, HostedWorkers{"web": {discovery.DefaultRootDirName + "/__init__.py:1"}})
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "python") {
		t.Fatalf("apps() = %v, want the python worker refused naming the app", err)
	}
}

func keysOf(entries map[string][]byte) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}
