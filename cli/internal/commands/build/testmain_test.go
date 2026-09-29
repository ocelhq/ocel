package build

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if os.Getenv(clitest.FakeProviderEnvVar) == "1" {
		os.Exit(clitest.RunFakeProvider())
	}
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{Invocation: clitest.NewInvocation(), BuildApps: build.Apps}
}

func stubBuild(dependencies *Dependencies, functions []build.Function) {
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
}
