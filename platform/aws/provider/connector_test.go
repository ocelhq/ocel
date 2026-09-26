package aws

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestAnUnsetComputeTakesTheProvidersOwnDefault(t *testing.T) {
	t.Parallel()

	compute, err := provider.ConnectorCompute("", connectorCompute)
	if err != nil {
		t.Fatalf("provider.ConnectorCompute(\"\", connectorCompute) = %v, want the provider to pick for itself", err)
	}
	if compute != provider.ComputeServerless {
		t.Errorf("provider.ConnectorCompute(\"\", connectorCompute) = %q, want %q", compute, provider.ComputeServerless)
	}
}

func TestAComputeThisAccountDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := provider.ConnectorCompute(provider.ComputeContainer, connectorCompute)
	if err == nil {
		t.Fatal("ConnectorCompute(container) = nil, want the compute no aws connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(provider.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}
