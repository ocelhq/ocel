package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"

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
  apps: [{ name: "api", path: ".", framework: "node" }],
};
`)

		var built *projectconfig.Config
		deps := newDeps()
		deps.BuildApps = func(_ context.Context, cfg *projectconfig.Config, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
			built = cfg
			return build.Output{Functions: []manifestbuilder.Function{{Route: "index", App: "api"}}}, nil
		}

		var stdout, stderr bytes.Buffer
		if err := runBuild(context.Background(), deps, root, &stdout, &stderr); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if got, want := stdout.String(), "Built 1 function into "+constants.ProjectStateDirName+"/output\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
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
				Functions: []manifestbuilder.Function{{Route: "index", App: "api"}},
				Images:    map[string]string{"web": clitest.FixtureImage("web")},
			}, nil
		}

		var stdout bytes.Buffer
		if err := runBuild(context.Background(), deps, root, &stdout, io.Discard); err != nil {
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

		err := runBuild(context.Background(), deps, root, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "boom: app build failed") {
			t.Fatalf("runBuild err = %v, want the build failure surfaced", err)
		}
	})
}
