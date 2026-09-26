package connectorkit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const configJSON = `{"console":"https://console.example.com","connectorId":"con_1","organizationId":"org_1","grants":["envvars.read"]}`

func TestAFormWithNoFilesystemReadsItsConfigOffTheEnvironment(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, configJSON)

	config, err := ReadConfig("")
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if config.Console != "https://console.example.com" || config.ConnectorID != "con_1" || config.OrganizationID != "org_1" {
		t.Errorf("config = %+v, want what the environment set", config)
	}
	if config.KeyPath != "" {
		t.Errorf("config names a key at %q, and a form with no filesystem has no identity on disk", config.KeyPath)
	}
}

func TestTheEnvironmentIsReadAheadOfAPathThatNamesNothing(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, configJSON)

	if _, err := ReadConfig(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("ReadConfig with the environment set = %v, want the config from the environment read", err)
	}
}

func TestWithNeitherAPathNorTheEnvironmentNothingNamesTheConsole(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, "")

	_, err := ReadConfig("")
	if err == nil {
		t.Fatal("ReadConfig = nil, want a refusal naming where the config would have come from")
	}
	if !strings.Contains(err.Error(), provider.ConnectorConfigEnvVar) {
		t.Errorf("err = %v, want it to name %s", err, provider.ConnectorConfigEnvVar)
	}
}

func TestAPathThatNamesNoFileIsRefused(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, "")

	absent := filepath.Join(t.TempDir(), "absent.json")
	_, err := ReadConfig(absent)
	if err == nil {
		t.Fatal("ReadConfig = nil, want a refusal: the path names no file and nothing else supplies the config")
	}
	if !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("err = %v, want it to name the path it could not read", err)
	}
}

func TestAConfigOnDiskIsStillRead(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, "")

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if config.ConnectorID != "con_1" {
		t.Errorf("config = %+v, want what the file contains", config)
	}
}

func TestAConnectorWithNoKeyStillServes(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig([]byte(configJSON), "test")
	if err != nil {
		t.Fatal(err)
	}
	mux, err := Mux(Spec{Config: cfg, Version: "0.0.0", Vendor: "aws"})
	if err != nil {
		t.Fatalf("Mux with no identity = %v, want a mux a probed form can serve", err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/v1/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the capabilities probe answered %s, and nothing answers it without the console's token", resp.Status)
	}
}
