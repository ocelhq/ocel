package root

import (
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

const rootArgsEnvVar = "OCEL_TEST_ROOT_ARGS"

func runRootSubprocess(args []string) int {
	return newCommand().exit(args)
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

	dir, err := os.MkdirTemp("", "ocel-cli-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Unsetenv("OCEL_CONFIG")
	os.Unsetenv("OCEL_JSON")
	os.Unsetenv("OCEL_DEBUG")
	clitest.UnsetColorEnv()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
