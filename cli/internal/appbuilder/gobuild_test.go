package appbuilder

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/providerkit"
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

func TestAGoAppIsCompiledHereRatherThanHandedToTheNodeBuilder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuilder(t, root)
	writeGoApp(t, root, "apps/api")
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Runtime: projectconfig.Runtime{Name: "go"}}},
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
		t.Error("the node builder was run for a go app, and nothing it knows how to build was in the request")
	}

	fns, err := CollectFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "CollectFunctions", fns, []manifestbuilder.Function{{
		Route:        "index",
		Runtime:      manifestbuilder.Runtime{Name: "go", Arch: providerkit.ArchX8664},
		Handler:      "api",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	binary := filepath.Join(root, ".ocel", "output", "apps", "api", "functions", "index.func", "api")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("the build wrote no binary for the function to boot: %v", err)
	}
}
