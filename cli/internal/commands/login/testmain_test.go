package login

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{
		Invocation:        clitest.NewInvocation(),
		LoadCredentials:   console.LoadCredentials,
		SaveCredentials:   console.SaveCredentials,
		DeleteCredentials: console.DeleteCredentials,
	}
}
