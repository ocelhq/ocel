package deploy

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestMain(m *testing.M) {
	if os.Getenv(clitest.FakeProviderEnvVar) == "1" {
		os.Exit(clitest.RunFakeProvider())
	}
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}
