package main

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

var refusedOnTheBox = map[string]string{
	contractv1connect.ProviderServiceProvisionInfraProcedure:          "the box runs no provisioning engine",
	contractv1connect.ProviderServiceDeployProcedure:                  "the box runs no provisioning engine",
	contractv1connect.ProviderServicePreflightProcedure:               "a deploy's preflight runs where the deploy does",
	contractv1connect.ProviderServiceBootstrapProcedure:               "a bootstrap needs root over the person's own ssh",
	contractv1connect.ProviderServiceDescribeBootstrapProcedure:       "a bootstrap needs root over the person's own ssh",
	contractv1connect.ProviderServiceRemoveBootstrapProcedure:         "a bootstrap needs root over the person's own ssh",
	contractv1connect.ProviderServicePlanRemoveBootstrapProcedure:     "a bootstrap needs root over the person's own ssh",
	contractv1connect.ProviderServiceForwardPortsProcedure:            "forwarding ports runs where the person is",
	contractv1connect.ProviderServiceDescribeConnectorTargetProcedure: "the connector never describes itself",
	contractv1connect.ProviderServiceInstallConnectorProcedure:        "a connector never installs itself",
	contractv1connect.ProviderServiceRemoveConnectorProcedure:         "a connector never removes itself",
}

func hostedProcedures() []protoreflect.MethodDescriptor {
	var methods []protoreflect.MethodDescriptor
	for _, service := range []protoreflect.ServiceDescriptor{
		contractv1.File_provider_contract_v1_contract_proto.Services().ByName("ProviderService"),
		costv1.File_provider_cost_v1_cost_proto.Services().ByName("CostService"),
	} {
		for i := range service.Methods().Len() {
			methods = append(methods, service.Methods().Get(i))
		}
	}
	return methods
}

func procedureOf(method protoreflect.MethodDescriptor) string {
	return "/" + string(method.Parent().FullName()) + "/" + string(method.Name())
}

func TestEveryProcedureTheBoxHostsIsServedOrDeliberatelyRefused(t *testing.T) {
	servedHere := vps.ControlServedProcedures()
	known := map[string]bool{}
	for _, method := range hostedProcedures() {
		procedure := procedureOf(method)
		known[procedure] = true
		_, refused := refusedOnTheBox[procedure]
		switch isServed := slices.Contains(servedHere, procedure); {
		case isServed && refused:
			t.Errorf("%s is both served on the box and refused there: decide which", procedure)
		case !isServed && !refused:
			t.Errorf("%s is neither served on the box nor refused there: add it to vps.ControlServedProcedures, or to refusedOnTheBox with why", procedure)
		}
	}
	for _, procedure := range slices.Concat(servedHere, slices.Collect(maps.Keys(refusedOnTheBox))) {
		if !known[procedure] {
			t.Errorf("%s is named for the box, and no hosted service has it", procedure)
		}
	}
}

func TestEveryProcedureRefusedOnTheBoxIsRefusedAsNeedingARunner(t *testing.T) {
	onEndpoint := endpointServing(t, &panics{})
	sessionless := http.Header{providerhost.HeaderSlug: {"shop"}, providerhost.HeaderTier: {"production"}}
	for _, method := range hostedProcedures() {
		if _, refused := refusedOnTheBox[procedureOf(method)]; !refused {
			continue
		}
		err := call(context.Background(), onEndpoint, method, sessionless, 1)
		if code, _ := provider.RefusedCode(err); code != refusal.CodeNeedsRunner {
			t.Errorf("%s on the box = %v, want it refused as needing a runner", procedureOf(method), err)
		}
	}
}
