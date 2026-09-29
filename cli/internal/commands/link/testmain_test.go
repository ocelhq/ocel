package link

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}
