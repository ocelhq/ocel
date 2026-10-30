package endpoint

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const configJSON = `{"console":"https://console.example.com","endpointId":"con_1","organizationId":"org_1","allowedScopes":["variables.read"]}`

func TestAFormWithNoFilesystemReadsItsConfigOffTheEnvironment(t *testing.T) {
	t.Setenv(ConfigEnvVar, configJSON)

	config, err := ReadConfig("")
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if config.Console != "https://console.example.com" || config.EndpointID != "con_1" || config.OrganizationID != "org_1" {
		t.Errorf("config = %+v, want what the environment set", config)
	}
	if config.KeyPath != "" {
		t.Errorf("config names a key at %q, and a form with no filesystem has no identity on disk", config.KeyPath)
	}
}

func TestTheEnvironmentIsReadAheadOfAPathThatNamesNothing(t *testing.T) {
	t.Setenv(ConfigEnvVar, configJSON)

	if _, err := ReadConfig(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("ReadConfig with the environment set = %v, want the config from the environment read", err)
	}
}

func TestWithNeitherAPathNorTheEnvironmentNothingNamesTheConsole(t *testing.T) {
	t.Setenv(ConfigEnvVar, "")

	_, err := ReadConfig("")
	if err == nil {
		t.Fatal("ReadConfig = nil, want a refusal naming where the config would have come from")
	}
	if !strings.Contains(err.Error(), ConfigEnvVar) {
		t.Errorf("err = %v, want it to name %s", err, ConfigEnvVar)
	}
}

func TestAPathThatNamesNoFileIsRefused(t *testing.T) {
	t.Setenv(ConfigEnvVar, "")

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
	t.Setenv(ConfigEnvVar, "")

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if config.EndpointID != "con_1" {
		t.Errorf("config = %+v, want what the file contains", config)
	}
}

func TestAnEndpointWithNoKeyStillServesAndRefusesAnUnsignedCall(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig([]byte(configJSON), "test")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Spec{Config: cfg, Version: "0.0.0", Vendor: "aws", Mounts: []Mount{variablesOnFakes()}})
	if err != nil {
		t.Fatalf("New with no identity = %v, want a handler a probed form can serve", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	_, err = variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL).
		ListValues(t.Context(), &variablestorev1.ListValuesRequest{})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an unsigned call = %v, want it refused as unauthenticated", err)
	}
}

func TestAConfigAllowingAScopeNoPersonHoldsIsRefused(t *testing.T) {
	t.Parallel()

	for _, claim := range []string{"job", "target.credential", "cloud.provider", "SCOPE_JOB"} {
		raw := `{"console":"https://console.example.com","endpointId":"end_1","organizationId":"org_1","allowedScopes":["` + claim + `"]}`
		if _, err := ParseConfig([]byte(raw), "test"); err == nil {
			t.Errorf("ParseConfig allowing %q = nil, want it refused: only a token's kind grants a non-user scope", claim)
		}
	}
}

func variablesOnFakes() Mount {
	return VariablesMount(variablestoreserver.FixedBackend{KeyValues: fake.NewKeyValues(), Cipher: fake.NewCipher()})
}
