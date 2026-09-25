package connectorkit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const carried = `{"console":"https://console.example.com","connectorId":"con_1","organizationId":"org_1","grants":["envvars.read"]}`

func TestAFormWithNoFilesystemReadsItsConfigOffTheEnvironment(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, carried)

	held, err := ReadConfig("")
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if held.Console != "https://console.example.com" || held.ConnectorID != "con_1" || held.OrganizationID != "org_1" {
		t.Errorf("config = %+v, want what the environment carried", held)
	}
	if held.KeyPath != "" {
		t.Errorf("config names a key at %q, and a form with no filesystem holds no standing identity", held.KeyPath)
	}
}

func TestTheEnvironmentIsReadAheadOfAPathThatNamesNothing(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, carried)

	if _, err := ReadConfig(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("ReadConfig with the environment set = %v, want the carried config read", err)
	}
}

func TestWithNeitherAPathNorTheEnvironmentNothingNamesTheConsole(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, "")

	_, err := ReadConfig("")
	if err == nil {
		t.Fatal("ReadConfig = nil, want a refusal naming what would have carried the config")
	}
	if !strings.Contains(err.Error(), providerkit.ConnectorConfigEnvVar) {
		t.Errorf("err = %v, want it to name %s", err, providerkit.ConnectorConfigEnvVar)
	}
}

func TestAPathThatNamesNoFileIsRefused(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, "")

	absent := filepath.Join(t.TempDir(), "absent.json")
	_, err := ReadConfig(absent)
	if err == nil {
		t.Fatal("ReadConfig = nil, want a refusal: the path names no file and nothing else carries the config")
	}
	if !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("err = %v, want it to name the path it could not read", err)
	}
}

func TestAConfigOnDiskIsStillRead(t *testing.T) {
	t.Setenv(providerkit.ConnectorConfigEnvVar, "")

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(carried), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if held.ConnectorID != "con_1" {
		t.Errorf("config = %+v, want what the file held", held)
	}
}

func TestAConnectorThatHoldsNoKeyStillServes(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig([]byte(carried), "test")
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
