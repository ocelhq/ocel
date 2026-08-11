package cli

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/credentials"
)

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

func setLoggedIn(d *deps) {
	d.loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
}
