package userconfig_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const printInstallIDEnvVar = "OCEL_TEST_PRINT_INSTALL_ID"

func TestMain(m *testing.M) {
	if os.Getenv(printInstallIDEnvVar) != "" {
		id, err := userconfig.EnsureInstallID()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(id)
		os.Exit(0)
	}
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}
