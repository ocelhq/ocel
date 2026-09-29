package cost

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
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
	return Dependencies{Invocation: clitest.NewInvocation(), ReadFunctions: build.ReadFunctions, CollectDeclarations: declaration.Collect}
}

func stubFunctions(dependencies *Dependencies, functions []build.Function) {
	dependencies.ReadFunctions = func(string) ([]build.Function, error) {
		return functions, nil
	}
}
