package cli

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/credentials"
)

// TestMain points os.UserConfigDir() (via XDG_CONFIG_HOME) at a scratch
// directory for the whole test binary, so tests exercising runDev's default
// provisioning path - which now goes through provision.CachedResolve's
// on-disk cache - don't read or write the real user's ~/.config/ocel.
//
// It also dispatches to runDeployFakeProvider when this binary is re-exec'd
// as a fake provider (see deploy_fakeprovider_test.go), Go's helper-process
// pattern as used by providerrunner's own TestMain.
func TestMain(m *testing.M) {
	if os.Getenv(deployFakeProviderEnvVar) == "1" {
		os.Exit(runDeployFakeProvider())
	}

	dir, err := os.MkdirTemp("", "ocel-cli-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// setLoggedIn overrides the loadCredentials seam for the duration of t so the
// commands that still require Ocel Cloud see a logged-in user, restoring the
// previous value on cleanup.
func setLoggedIn(t *testing.T) {
	t.Helper()
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	t.Cleanup(func() { loadCredentials = prev })
}
