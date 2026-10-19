package build

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workdir"
)

func TestBuildHoldsTheBuildOutputWhileItBuilds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", compute: "serverless" }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "package.json"), `{"name":"web"}`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), "export {};\n")

	dependencies := newTestDependencies()
	var held bool
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		tried, cancel := context.WithCancel(ctx)
		cancel()
		release, err := workdir.LockBuildOutput(tried, cfg.Dir, func() {})
		if err == nil {
			release()
		}
		held = err != nil
		return build.Output{}, nil
	}
	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if !held {
		t.Error("another run could take the build output while ocel build wrote it, want it held")
	}
}
