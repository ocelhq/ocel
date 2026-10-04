package bindings

import (
	"io"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/terminal"
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

var testFormat = terminal.FormatHuman

func useJSONOutput(t *testing.T) {
	t.Helper()
	orig := testFormat
	t.Cleanup(func() { testFormat = orig })
	testFormat = terminal.FormatJSON
}

func newTestInvocation() commands.Invocation {
	invocation := clitest.NewInvocation()
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: testFormat})
	}
	return invocation
}
