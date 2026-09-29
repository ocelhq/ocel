package dev

import (
	"os"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	watchDebounce = 20 * time.Millisecond
	os.Unsetenv("OCEL_CONFIG")
	os.Exit(m.Run())
}
