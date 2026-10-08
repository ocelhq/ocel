package conformance

import (
	"fmt"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func RunFunctionBuild(t *testing.T, facts provider.Facts) {
	t.Helper()

	for _, fault := range findFunctionBuildFaults(facts) {
		t.Error(fault)
	}
}

func findFunctionBuildFaults(facts provider.Facts) []string {
	var found []string
	if facts.MaxFunctionBytes < 0 {
		found = append(found, fmt.Sprintf("Facts.MaxFunctionBytes is %d, which no function fits: name a positive budget, or zero for none", facts.MaxFunctionBytes))
	}
	return found
}

func answersTheFunctionBuildFacts(t *testing.T, suite Suite, configured *contractv1.ProviderFacts) {
	t.Helper()

	p := readDeclaredProvider(t, suite)
	declared := p.Facts()
	if got, want := configured.GetShipsNextServerRuntime(), p.Hooks().ReadNextServerRuntime != nil; got != want {
		t.Errorf("ConfigureResponse.facts.ships_next_server_runtime = %v, want %v — the RPC answers whether Hooks().ReadNextServerRuntime is set", got, want)
	}
	if got, want := configured.GetForwardsPorts(), p.Hooks().ForwardPorts != nil; got != want {
		t.Errorf("ConfigureResponse.facts.forwards_ports = %v, want %v — the RPC answers whether Hooks().ForwardPorts is set", got, want)
	}
	if got, want := configured.GetMaxFunctionBytes(), declared.MaxFunctionBytes; got != want {
		t.Errorf("ConfigureResponse.facts.max_function_bytes = %d, want %d — the RPC answers what Facts().MaxFunctionBytes declares", got, want)
	}
	if got, want := configured.GetNextRefreshesByRequest(), declared.NextRefreshesByRequest; got != want {
		t.Errorf("ConfigureResponse.facts.next_refreshes_by_request = %t, want %t — the RPC answers what Facts().NextRefreshesByRequest declares", got, want)
	}
}
