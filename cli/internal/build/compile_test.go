package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func writeGoApp(t *testing.T, root, path string) {
	t.Helper()
	dir := filepath.Join(root, path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":  "module fixture\n\ngo 1.24\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAGoAppIsCompiledHereRatherThanHandedToTheNodeBuildScript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	writeGoApp(t, root, "apps/api")
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "go"}}},
	}

	ran := false
	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Error("the node build script was run for a go app, and nothing it knows how to build was in the request")
	}

	fns, err := ReadFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "ReadFunctions", fns, []Function{{
		Route:        "index",
		Framework:    buildoutput.Framework{Name: "go", Arch: arch.X8664},
		EntryFile:    "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, statedir.Name, "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
	}
}

func TestAGoAppWhoseModuleDeclaresTasksCarriesTheWorkerAsASecondBinary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	fixture := filepath.Join(fixturetest.RepoDir(t), "tests", "fixtures", "worker", "go")
	module, err := os.ReadFile(filepath.Join(fixture, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module = []byte(strings.Replace(string(module), "=> ../../../../sdk", "=> "+filepath.Join(fixturetest.RepoDir(t), "sdk"), 1))
	sums, err := os.ReadFile(filepath.Join(fixture, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := os.ReadFile(filepath.Join(fixture, discovery.DefaultRootDirName, "infra.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "go.mod"), string(module))
	writeFile(t, filepath.Join(root, "go.sum"), string(sums))
	writeFile(t, filepath.Join(root, discovery.DefaultRootDirName, "infra.go"), string(declarations))
	writeFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "worker", Path: ".", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "go"}}},
	}

	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error { return nil }}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	functionDir := filepath.Join(root, statedir.Name, "output", "apps", "worker", "functions", "index.func")
	for _, binary := range []string{"worker", buildoutput.GoWorkerBinary} {
		if _, err := os.Stat(filepath.Join(functionDir, binary)); err != nil {
			t.Errorf("the artifact has no %s binary: %v", binary, err)
		}
	}
}

func TestAGoAppWhoseDiscoveryFolderIsAModuleOfItsOwnCarriesNoWorkerAndGetsNothingWrittenIntoThatModule(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	writeGoApp(t, root, ".")
	writeFile(t, filepath.Join(root, discovery.DefaultRootDirName, "go.mod"), "module fixture/infra\n\ngo 1.24\n")
	writeFile(t, filepath.Join(root, discovery.DefaultRootDirName, "infra.go"), "package infra\n")
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "api", Path: ".", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "go"}}},
	}

	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error { return nil }}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, discovery.DefaultRootDirName, statedir.Name)); !os.IsNotExist(err) {
		t.Errorf("the build wrote a worker main into %s, a module the app's binary does not compile: %v", discovery.DefaultRootDirName, err)
	}
	binary := filepath.Join(root, statedir.Name, "output", "apps", "api", "functions", "index.func", buildoutput.GoWorkerBinary)
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Errorf("the artifact carries %s, and nothing in the app's module declares through ocel: %v", buildoutput.GoWorkerBinary, err)
	}
}

func writePythonApp(t *testing.T, root, path string) {
	t.Helper()
	dir := filepath.Join(root, path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAPythonAppIsVendoredHereRatherThanHandedToTheNodeBuilder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	writePythonApp(t, root, "apps/api")
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "python"}}},
	}

	ran := false
	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Error("the node build script was run for a python app, and nothing it knows how to build was in the request")
	}

	fns, err := ReadFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "ReadFunctions", fns, []Function{{
		Route:        "index",
		Framework:    buildoutput.Framework{Name: "python", Arch: arch.X8664},
		EntryFile:    "main.py",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	entry := filepath.Join(root, statedir.Name, "output", "apps", "api", "functions", "index.func", "main.py")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the build produced no module for the function to boot: %v", err)
	}
}

func TestARustAppIsCompiledHereRatherThanHandedToTheNodeBuilder(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}

	root := t.TempDir()
	writeBuildScript(t, root)
	dir := filepath.Join(root, "apps", "api")
	for name, body := range map[string]string{
		"Cargo.toml":  "[package]\nname = \"api\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n",
		"src/main.rs": "fn main() {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "rust"}}},
	}

	ran := false
	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Error("the node build script was run for a rust app, and nothing it knows how to build was in the request")
	}

	fns, err := ReadFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "ReadFunctions", fns, []Function{{
		Route:        "index",
		Framework:    buildoutput.Framework{Name: "rust", Arch: arch.X8664},
		EntryFile:    "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, statedir.Name, "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
	}
}
