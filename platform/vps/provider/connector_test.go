package vps

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnUnsetComputeTakesTheBoxsStandingProcess(t *testing.T) {
	t.Parallel()

	held, err := connectorComputeOf("")
	if err != nil {
		t.Fatalf("connectorComputeOf(\"\") = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeContainer {
		t.Errorf("connectorComputeOf(\"\") = %q, want %q", held, providerkit.ComputeContainer)
	}
}

func TestAComputeNoMachineHandsOutIsRefusedByTheProvider(t *testing.T) {
	t.Parallel()

	_, err := connectorComputeOf(providerkit.ComputeServerless)
	if err == nil {
		t.Fatal("connectorComputeOf(serverless) = nil, want a box to refuse a compute it cannot hand out")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeServerless)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}
