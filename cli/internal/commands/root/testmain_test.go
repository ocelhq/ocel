package root

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

const rootArgsEnvVar = "OCEL_TEST_ROOT_ARGS"

func runRootSubprocess(args []string) int {
	ocel := newCommand()
	ocel.root.SetArgs(args)
	err := ocel.execute()
	if err == nil {
		return 0
	}
	if code, ok := exitcode.Of(err); ok {
		return code
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	return 1
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
	clitest.UnsetColorEnv()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
