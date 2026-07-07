package provision

import (
	"os"
	"testing"
)

// TestMain points os.UserConfigDir() (via XDG_CONFIG_HOME) at a scratch
// directory for the whole test binary, so tests exercising Provision's
// default openCache don't read or write the real user's ~/.config/ocel.
// Tests that need tighter control over the cache override the openCache var
// directly instead.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ocel-provision-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
