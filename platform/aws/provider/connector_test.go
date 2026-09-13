package provider

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnUnsetComputeTakesTheProvidersOwnDefault(t *testing.T) {
	t.Parallel()

	held, err := connectorComputeOf("")
	if err != nil {
		t.Fatalf("connectorComputeOf(\"\") = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeServerless {
		t.Errorf("connectorComputeOf(\"\") = %q, want %q", held, providerkit.ComputeServerless)
	}
}

func TestAComputeThisAccountDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := connectorComputeOf(providerkit.ComputeContainer)
	if err == nil {
		t.Fatal("connectorComputeOf(container) = nil, want the compute no aws connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}
