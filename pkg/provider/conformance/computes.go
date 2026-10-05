package conformance

import (
	"context"
	"slices"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func namesTheComputesItRuns(t *testing.T, suite Suite, facts *contractv1.ProviderFacts) {
	t.Helper()

	served := facts.GetComputes()
	if len(served) == 0 {
		t.Fatal("ProviderFacts.computes is empty, and a provider that names no compute can run nothing")
	}

	seen := make(map[string]bool, len(served))
	for _, compute := range served {
		if seen[compute] {
			t.Errorf("ProviderFacts.computes names %q twice, and the first entry is the default, so a repeat leaves the default ambiguous", compute)
		}
		seen[compute] = true
		if !provider.KnownCompute(compute) {
			t.Errorf("ProviderFacts.computes names %q, which is no compute the contract knows: %v", compute, provider.ComputeNames(provider.Computes()))
		}
	}

	declared := computesDeclared(t, suite)
	if want := provider.ComputeNames(declared); !slices.Equal(served, want) {
		t.Errorf("ProviderFacts.computes = %v, want %v — the RPC answers what Computes() answers, order included", served, want)
	}
}

func computesDeclared(t *testing.T, suite Suite) []provider.Compute {
	t.Helper()

	if suite.Server.New == nil {
		t.Fatal("the suite has no Spec.New, so nothing can read Computes() back off the provider the RPC server is serving")
	}
	p, err := suite.Server.New(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider to read Computes() from", err)
	}
	return p.Facts().Computes
}
