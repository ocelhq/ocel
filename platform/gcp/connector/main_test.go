package main

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestTheConnectorRefusesToStartWithNoAccountToAdmitHeartbeatsAs(t *testing.T) {
	t.Setenv(provider.ConnectorConfigEnvVar, `{"console":"https://console.example.com","connectorId":"conn-1","organizationId":"org-1"}`)
	t.Setenv(provider.NamespaceEnvVar, "ocel")
	t.Setenv(ports.ProjectEnvVar, "acme-prod")
	t.Setenv(ports.RegionEnvVar, "europe-west1")
	t.Setenv(ports.ConnectorAccountEnvVar, "")

	err := run("127.0.0.1:0", "", false)
	if err == nil || !strings.Contains(err.Error(), ports.ConnectorAccountEnvVar) {
		t.Errorf("run() = %v, want it refused naming %s: with no account, no Cloud Scheduler token could be told from a stranger's", err, ports.ConnectorAccountEnvVar)
	}
}

func TestTheConnectorProvesItsIdentityAsItsOwnServiceAccount(t *testing.T) {
	if variableStore(&ports.Clients{}).ProveIdentity == nil {
		t.Fatal("the connector's backend has no ProveIdentity, so an Infisical env source with identity auth is refused through the console")
	}
}
