package domain

import (
	"io"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/terminal"
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

var testLogFormat = terminal.FormatHuman

func useJSONOutput(t *testing.T) {
	t.Helper()
	orig := testLogFormat
	t.Cleanup(func() { testLogFormat = orig })
	testLogFormat = terminal.FormatJSON
}

func newTestDeps() cmddeps.Deps {
	deps := clitest.NewDeps()
	deps.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: testLogFormat})
	}
	return deps
}
