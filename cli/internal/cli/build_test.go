package cli

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func TestRunBuild(t *testing.T) {
	t.Parallel()

	t.Run("builds without login or provider", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "node" }],
};
`)

		var built *projectconfig.Config
		deps := newDeps()
		deps.BuildApps = func(_ context.Context, cfg *projectconfig.Config, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
			built = cfg
			return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
		}

		var stdout bytes.Buffer
		deps.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runBuild(context.Background(), deps, root); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if want := "✓ Built 1 function into " + constants.ProjectStateDirName + "/output in "; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the summary %q", stdout.String(), want)
		}

		record, err := os.ReadFile(filepath.Join(root, constants.ProjectStateDirName, "output", "client-digests.json"))
		if err != nil {
			t.Fatalf("the build recorded nothing about its client values: %v", err)
		}
		if !strings.Contains(string(record), `"api":{}`) {
			t.Errorf("client-digests.json = %s, want it to record the app with nothing inlined: `ocel build` resolves no declared value", record)
		}
	})

	t.Run("builds a container app's image and names what it built", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [
    { name: "api", path: "api", framework: "node" },
    { name: "web", path: "web", compute: "container", arch: "arm64" },
  ],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "api", "index.js"), "export {};\n")
		clitest.WriteFile(t, filepath.Join(root, "web", "server.js"), "export {};\n")

		var archs map[string]string
		deps := newDeps()
		deps.BuildApps = func(_ context.Context, _ *projectconfig.Config, _ map[string]map[string]string, asked map[string]string, _ build.Log) (build.Output, error) {
			archs = asked
			return build.Output{
				Functions: []build.Function{{Route: "index", App: "api"}},
				Images:    map[string]string{"web": clitest.FixtureImage("web")},
			}, nil
		}

		var stdout bytes.Buffer
		deps.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runBuild(context.Background(), deps, root); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if want := map[string]string{"web": "arm64"}; !maps.Equal(archs, want) {
			t.Errorf("the build was asked for images of %v, want %v: a container app is built for the architecture it declares", archs, want)
		}
		for _, want := range []string{"1 function", "web", clitest.FixtureImage("web")} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q", stdout.String(), want)
			}
		}
	})

	t.Run("bakes each app's plaintext client values, as a deploy does", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "next", domains: { production: ["shop.acme.com"] } }],
};
`)

		var env map[string]map[string]string
		deps := newDeps()
		deps.BuildApps = func(_ context.Context, _ *projectconfig.Config, handed map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
			env = handed
			return build.Output{}, nil
		}

		deps.Events = run.NewBus(time.Now)
		if err := runBuild(context.Background(), deps, root); err != nil {
			t.Fatalf("runBuild: %v", err)
		}
		if got, want := env["web"][appbuild.ClientURLEnvName], "https://shop.acme.com"; got != want {
			t.Errorf("web was built with %s = %q, want %q", appbuild.ClientURLEnvName, got, want)
		}
	})

	t.Run("surfaces a build failure", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		deps := newDeps()
		deps.BuildApps = func(context.Context, *projectconfig.Config, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
			return build.Output{}, errors.New("boom: app build failed")
		}

		var stdout bytes.Buffer
		deps.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runBuild(context.Background(), deps, root); err == nil {
			t.Fatal("runBuild err = nil, want the build to fail")
		}
		if !strings.Contains(stdout.String(), "✗ Build failed in ") || !strings.Contains(stdout.String(), "boom: app build failed") {
			t.Fatalf("stdout = %q, want the build failure surfaced in the run's summary", stdout.String())
		}
	})
}
