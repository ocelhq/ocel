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
	writeBuilder(t, root)
	writePythonApp(t, root, "apps/api")
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Runtime: projectconfig.Runtime{Name: "python"}}},
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
		t.Error("the node builder was run for a python app, and nothing it knows how to build was in the request")
	}

	fns, err := CollectFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}
	assertFunctions(t, "CollectFunctions", fns, []manifestbuilder.Function{{
		Route:        "index",
		Runtime:      manifestbuilder.Runtime{Name: "python", Arch: providerkit.ArchX8664},
		Handler:      "main.py",
		ArtifactPath: "apps/api/functions/index.func",
		RouteID:      "/",
		App:          "api",
	}})

	entry := filepath.Join(root, ".ocel", "output", "apps", "api", "functions", "index.func", "main.py")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the build carried no module for the function to boot: %v", err)
	}
}
