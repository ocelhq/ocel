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

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRunBuild(t *testing.T) {
	t.Parallel()

	t.Run("builds without login or provider", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "express" }],
};
`)

		var built *projectconfig.Config
		deps := newDeps()
		deps.BuildApp = func(_ context.Context, cfg *projectconfig.Config, _ map[string]map[string]string, _ string, _ io.Writer) error {
			built = cfg
			clitest.WritePrebuiltFunction(t, cfg.Dir, "api", "index")
			return nil
		}

		var stdout, stderr bytes.Buffer
		if err := runBuild(context.Background(), deps, root, &stdout, &stderr); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if got, want := stdout.String(), "Built 1 function into .ocel/output\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}

		record, err := os.ReadFile(filepath.Join(root, ".ocel", "output", "client-digests.json"))
		if err != nil {
			t.Fatalf("the build recorded nothing about its client values: %v", err)
		}
		if !strings.Contains(string(record), `"resolved":false`) {
			t.Errorf("client-digests.json = %s, want it to state that the build resolved no values", record)
		}
	})

	t.Run("surfaces a build failure", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		deps := newDeps()
		deps.BuildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, string, io.Writer) error {
			return errors.New("boom: app build failed")
		}

		err := runBuild(context.Background(), deps, root, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "boom: app build failed") {
			t.Fatalf("runBuild err = %v, want the build failure surfaced", err)
		}
	})
}
