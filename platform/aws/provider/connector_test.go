package provider

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnUnsetComputeTakesTheProvidersOwnDefault(t *testing.T) {
	t.Parallel()

	held, err := providerkit.ConnectorCompute("", connectorCompute)
	if err != nil {
		t.Fatalf("providerkit.ConnectorCompute(\"\", connectorCompute) = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeServerless {
		t.Errorf("providerkit.ConnectorCompute(\"\", connectorCompute) = %q, want %q", held, providerkit.ComputeServerless)
	}
}

func TestAComputeThisAccountDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := providerkit.ConnectorCompute(providerkit.ComputeContainer, connectorCompute)
	if err == nil {
		t.Fatal("ConnectorCompute(container) = nil, want the compute no aws connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}
