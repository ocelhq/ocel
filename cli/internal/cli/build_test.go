package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestRunBuild_BuildsWithoutLoginOrProvider(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "express" }],
};
`)

	var built *projectconfig.Config
	prev := buildApp
	buildApp = func(_ context.Context, cfg *projectconfig.Config, _ map[string]map[string]string, _ io.Writer) error {
		built = cfg
		writePrebuiltFunction(t, cfg.Dir, "api", "index")
		return nil
	}
	t.Cleanup(func() { buildApp = prev })

	var stdout, stderr bytes.Buffer
	if err := runBuild(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runBuild: %v", err)
	}

	if built == nil {
		t.Fatal("runBuild did not build the project")
	}
	if got, want := stdout.String(), "Built 1 function into .ocel/output\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	// The output states that this build resolved nothing, which is what lets a
	// later --prebuilt deploy say a client value was never inlined rather than
	// accuse it of having changed.
	record, err := os.ReadFile(filepath.Join(root, ".ocel", "output", "client-values.json"))
	if err != nil {
		t.Fatalf("the build recorded nothing about its client values: %v", err)
	}
	if !strings.Contains(string(record), `"resolved":false`) {
		t.Errorf("client-values.json = %s, want it to state that the build resolved no values", record)
	}
}

func TestRunBuild_BuildFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		return errors.New("boom: app build failed")
	}
	t.Cleanup(func() { buildApp = prev })

	err := runBuild(context.Background(), root, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "boom: app build failed") {
		t.Fatalf("runBuild err = %v, want the build failure surfaced", err)
	}
}
