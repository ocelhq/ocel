package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheConfigTheEnvironmentCarriesIsReadAheadOfTheFile(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, `{"console":"https://console.example.com","connectorId":"con_1","organizationId":"org_1","grants":["envvars.read"]}`)
	file := filepath.Join(t.TempDir(), "connector.json")
	if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run("127.0.0.1:0", file, false, false)
	if err == nil {
		t.Fatal("run() = nil, want the carried config refused for naming no target")
	}
	if !strings.Contains(err.Error(), "names no target") {
		t.Errorf("run() = %v, want the config the environment carried read and refused for its missing target, not the file beside it parsed", err)
	}
}
