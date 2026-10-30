package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

func TestTheConfigTheEnvironmentContainsIsReadAheadOfTheFile(t *testing.T) {
	t.Setenv(endpoint.ConfigEnvVar, `{"console":"https://console.example.com","endpointId":"con_1","organizationId":"org_1","allowedScopes":["variables.read"]}`)
	file := filepath.Join(t.TempDir(), "connector.json")
	if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run("127.0.0.1:0", file, false, false)
	if err == nil {
		t.Fatal("run() = nil, want the environment's config refused for naming no target")
	}
	if !strings.Contains(err.Error(), "names no target") {
		t.Errorf("run() = %v, want the config the environment contains read and refused for its missing target, not the file beside it parsed", err)
	}
}

func TestTheBoxServesTheVariableStore(t *testing.T) {
	services := endpoint.Spec{Mounts: []endpoint.Mount{variablesMount(t.TempDir())}}.Services()
	if len(services) != 1 || services[0] != variablestorev1connect.VariableStoreServiceName {
		t.Errorf("the box's endpoint serves %v, want the variable store", services)
	}
}
