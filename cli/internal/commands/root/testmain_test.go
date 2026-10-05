package root

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

const rootArgsEnvVar = "OCEL_TEST_ROOT_ARGS"

func runRootSubprocess(args []string) int {
	return newCommand().executeAndReport(args)
}

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	if os.Getenv(procTreeSessionHarnessEnvVar) == "1" {
		os.Exit(runProcessTreeSessionHarness())
	}
	if os.Getenv(procTreeModeEnvVar) != "" {
		os.Exit(runProcessTreeSubprocess())
	}
	if args, ok := os.LookupEnv(rootArgsEnvVar); ok {
		os.Exit(runRootSubprocess(strings.Split(args, " ")))
	}
	if slices.Equal(os.Args[1:], []string{"telemetry", "flush"}) {
		os.Exit(0)
	}

	done := clitest.IsolateConfigHome()
	os.Unsetenv("OCEL_JSON")
	os.Unsetenv("OCEL_DEBUG")
	os.Unsetenv("OCEL_TELEMETRY")
	os.Unsetenv("DO_NOT_TRACK")
	clitest.UnsetColorEnv()
	code := m.Run()
	done()
	os.Exit(code)
}
