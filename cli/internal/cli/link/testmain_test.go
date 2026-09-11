package link

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestMain(m *testing.M) {
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}
