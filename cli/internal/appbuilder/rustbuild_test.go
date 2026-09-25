package appbuilder

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestARustAppIsCompiledHereRatherThanHandedToTheNodeBuilder(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}

	root := t.TempDir()
	writeBuilder(t, root)
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
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: projectconfig.Framework{Name: "rust"}}},
	}

	ran := false
	builder := Builder{Exec: func(context.Context, string, []string, []byte, io.Writer) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Error("the node builder was run for a rust app, and nothing it knows how to build was in the request")
	}

	fns, err := CollectFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "CollectFunctions", fns, []manifestbuilder.Function{{
		Route:        "index",
		Framework:    manifestbuilder.Framework{Name: "rust", Arch: providerkit.ArchX8664},
		Handler:      "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
	}
}
