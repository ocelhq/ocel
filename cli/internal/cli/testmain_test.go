package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
)

const rootArgsEnvVar = "OCEL_TEST_ROOT_ARGS"

func runRootSubprocess(args []string) int {
	rootCmd.SetArgs(args)
	err := Execute()
	if err == nil {
		return 0
	}
	if code, ok := exitsig.ExitCode(err); ok {
		return code
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	return 1
}

func TestMain(m *testing.M) {
	if os.Getenv(clitest.FakeProviderEnvVar) == "1" {
		os.Exit(clitest.RunFakeProvider())
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
	os.Unsetenv("GITHUB_ACTIONS")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
