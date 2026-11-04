package main

import (
	"maps"
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

var refusedByTheConnector = map[string]string{
	contractv1connect.ProviderServiceProvisionInfraProcedure:      "the connector runs no provisioning engine",
	contractv1connect.ProviderServiceDeployProcedure:              "the connector runs no provisioning engine",
	contractv1connect.ProviderServicePreflightProcedure:           "a deploy's preflight runs where the deploy does",
	contractv1connect.ProviderServiceBootstrapProcedure:           "the connector runs no provisioning engine",
	contractv1connect.ProviderServiceRemoveBootstrapProcedure:     "the connector runs no provisioning engine",
	contractv1connect.ProviderServicePlanRemoveBootstrapProcedure: "the connector runs no provisioning engine",
	contractv1connect.ProviderServiceForwardPortsProcedure:        "forwarding ports runs where the person is",
	contractv1connect.ProviderServiceInstallConnectorProcedure:    "a connector never installs itself",
	contractv1connect.ProviderServiceRemoveConnectorProcedure:     "a connector never removes itself",
}

func TestEveryProcedureTheConnectorHostsIsServedOrDeliberatelyRefused(t *testing.T) {
	served := control.ServedProcedures()
	known := map[string]bool{}
	for _, service := range []protoreflect.ServiceDescriptor{
		contractv1.File_provider_contract_v1_contract_proto.Services().ByName("ProviderService"),
		costv1.File_provider_cost_v1_cost_proto.Services().ByName("CostService"),
	} {
		for i := range service.Methods().Len() {
			procedure := "/" + string(service.FullName()) + "/" + string(service.Methods().Get(i).Name())
			known[procedure] = true
			_, refused := refusedByTheConnector[procedure]
			switch isServed := slices.Contains(served, procedure); {
			case isServed && refused:
				t.Errorf("%s is both served by the connector and refused by it: decide which", procedure)
			case !isServed && !refused:
				t.Errorf("%s is neither served by the connector nor refused by it: add it to control.ServedProcedures, or to refusedByTheConnector with why", procedure)
			}
		}
	}
	for _, procedure := range slices.Concat(served, slices.Collect(maps.Keys(refusedByTheConnector))) {
		if !known[procedure] {
			t.Errorf("%s is named for the connector, and no hosted service has it", procedure)
		}
	}
}
