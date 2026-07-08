package devserver

import (
	"os"
	"testing"
)

// TestMain points os.UserConfigDir() (via XDG_CONFIG_HOME) at a scratch
// directory for the whole test binary, so tests exercising New's default
// provisioner - which now goes through provision.CachedResolve's on-disk
// cache - don't read or write the real user's ~/.config/ocel.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ocel-devserver-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
