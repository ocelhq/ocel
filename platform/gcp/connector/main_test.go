package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestTheConnectorRefusesToStartWithNoAccountToAdmitHeartbeatsAs(t *testing.T) {
	t.Setenv(endpoint.ConfigEnvVar, `{"console":"https://console.example.com","endpointId":"conn-1","organizationId":"org-1"}`)
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

func TestTheConnectorServesControlCallsAndVariables(t *testing.T) {
	host, err := providerhost.New(controlHost("acme-prod", "europe-west1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)

	want := []string{contractv1connect.ProviderServiceName, costv1connect.CostServiceName, variablestorev1connect.VariableStoreServiceName}
	if got := (endpoint.Spec{Mounts: mounts(host, &ports.Clients{})}).Services(); !slices.Equal(got, want) {
		t.Errorf("the connector serves %v, want %v", got, want)
	}
}

func TestTheConnectorOpensItsControlAssemblyInItsOwnProjectAndRegion(t *testing.T) {
	settings, err := controlHost("acme-prod", "europe-west1").Base(t.Context(), endpoint.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Options["project"] != "acme-prod" || settings.Options["region"] != "europe-west1" {
		t.Errorf("the base settings = %+v, want the project and region the connector runs in", settings.Options)
	}
}
