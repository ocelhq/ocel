package logs

import (
	"os"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	clitest.UnsetColorEnv()
	time.Local = time.UTC
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}
