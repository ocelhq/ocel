package appbuilder

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestAProjectWithNoJavaScriptNeverReachesForTheNodeBuilder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGoApp(t, root, "infra")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &projectconfig.Config{Dir: root}

	ran := false
	builder := Builder{Exec: func(context.Context, string, []string, []byte, io.Writer) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Fatal("the node builder ran for a project holding no JavaScript")
	}
}

func TestAJavaScriptProjectStillReachesTheNodeBuilderWithNoAppsDeclared(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuilder(t, root)
	cfg := &projectconfig.Config{Dir: root}

	ran := false
	builder := Builder{Exec: func(context.Context, string, []string, []byte, io.Writer) error {
		ran = true
		writePlan(t, filepath.Join(root, scratchDirName, outputDirName))
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !ran {
		t.Fatal("the node builder was skipped for a project the builder is what detects")
	}
}
