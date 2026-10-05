package telemetry_test

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestMain(m *testing.M) {
	done := clitest.IsolateConfigHome()
	os.Unsetenv("OCEL_TELEMETRY")
	os.Unsetenv("DO_NOT_TRACK")
	code := m.Run()
	done()
	os.Exit(code)
}
