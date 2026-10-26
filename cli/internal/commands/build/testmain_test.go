package build

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{Invocation: clitest.NewInvocation(), BuildApps: build.Apps, CollectDeclarations: declaringNothing}
}

func declaringNothing(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
	return nil, nil
}

func stubBuild(dependencies *Dependencies, functions []build.Function) {
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
}

func writeServerlessProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", compute: "serverless" }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "package.json"), `{"name":"web"}`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), "export {};\n")
	return root
}

type onStarted func(title string)

func (f onStarted) Receive(ev *streamv1.RunEvent) {
	if ev.GetOperation().GetStarted() != nil {
		f(ev.GetOperation().GetMessage())
	}
}

func (onStarted) Close() error { return nil }

func assertReportedUnder(t *testing.T, out, said, phase string) {
	t.Helper()
	for line := range strings.Lines(out) {
		if strings.Contains(line, said) {
			if !strings.Contains(line, "["+phase+"]") {
				t.Errorf("%q was reported as %q, want it under [%s]", said, line, phase)
			}
			return
		}
	}
	t.Errorf("stdout = %q, want %q reported under [%s]", out, said, phase)
}
