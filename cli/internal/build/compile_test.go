package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/constants"
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
		Apps: []project.App{{Name: "api", Path: "apps/api", Framework: project.Framework{Name: "go"}}},
	}

	ran := false
	builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
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
		Framework:    appbuild.Framework{Name: "go", Arch: arch.X8664},
		EntryFile:    "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
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
		Apps: []project.App{{Name: "api", Path: "apps/api", Framework: project.Framework{Name: "python"}}},
	}

	ran := false
	builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
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
		Framework:    appbuild.Framework{Name: "python", Arch: arch.X8664},
		EntryFile:    "main.py",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	entry := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", "functions", "index.func", "main.py")
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
		Apps: []project.App{{Name: "api", Path: "apps/api", Framework: project.Framework{Name: "rust"}}},
	}

	ran := false
	builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
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
		Framework:    appbuild.Framework{Name: "rust", Arch: arch.X8664},
		EntryFile:    "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
	}
}
