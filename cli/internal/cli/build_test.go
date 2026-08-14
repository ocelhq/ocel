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

func TestRunBuild(t *testing.T) {
	t.Parallel()

	t.Run("builds without login or provider", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "express" }],
};
`)

		var built *projectconfig.Config
		d := defaultDeps()
		d.buildApp = func(_ context.Context, cfg *projectconfig.Config, _ map[string]map[string]string, _ io.Writer) error {
			built = cfg
			writePrebuiltFunction(t, cfg.Dir, "api", "index")
			return nil
		}

		var stdout, stderr bytes.Buffer
		if err := runBuild(context.Background(), d, root, &stdout, &stderr); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if got, want := stdout.String(), "Built 1 function into .ocel/output\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}

		record, err := os.ReadFile(filepath.Join(root, ".ocel", "output", "client-values.json"))
		if err != nil {
			t.Fatalf("the build recorded nothing about its client values: %v", err)
		}
		if !strings.Contains(string(record), `"resolved":false`) {
			t.Errorf("client-values.json = %s, want it to state that the build resolved no values", record)
		}
	})

	t.Run("surfaces a build failure", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		d := defaultDeps()
		d.buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
			return errors.New("boom: app build failed")
		}

		err := runBuild(context.Background(), d, root, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "boom: app build failed") {
			t.Fatalf("runBuild err = %v, want the build failure surfaced", err)
		}
	})
}

func TestRunBuildDeployEnv(t *testing.T) {
	t.Run("hands the app build what OCEL_DEPLOY_ENV supplies", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "express" }],
};
`)
		t.Setenv("OCEL_DEPLOY_ENV", `{"MIDDLEWARE_TEST":"asdf"}`)

		var envByApp map[string]map[string]string
		d := defaultDeps()
		d.buildApp = func(_ context.Context, cfg *projectconfig.Config, env map[string]map[string]string, _ io.Writer) error {
			envByApp = env
			writePrebuiltFunction(t, cfg.Dir, "api", "index")
			return nil
		}

		if err := runBuild(context.Background(), d, root, io.Discard, io.Discard); err != nil {
			t.Fatalf("runBuild: %v", err)
		}
		if got := envByApp["api"]["MIDDLEWARE_TEST"]; got != "asdf" {
			t.Errorf("api build env = %v, want MIDDLEWARE_TEST=asdf", envByApp["api"])
		}
	})

	t.Run("refuses an OCEL_DEPLOY_ENV it cannot read", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		t.Setenv("OCEL_DEPLOY_ENV", `MIDDLEWARE_TEST=asdf`)

		d := defaultDeps()
		d.buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
			t.Error("runBuild built the app despite an unreadable OCEL_DEPLOY_ENV")
			return nil
		}

		err := runBuild(context.Background(), d, root, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "OCEL_DEPLOY_ENV") {
			t.Fatalf("runBuild err = %v, want it to name OCEL_DEPLOY_ENV", err)
		}
	})
}
