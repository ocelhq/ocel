package conformance

import (
	"context"
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func namesTheComputesItRuns(t *testing.T, suite Suite, providerClient contractv1connect.ProviderServiceClient) {
	t.Helper()

	resp, err := providerClient.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want the answer that names the computes this provider runs", err)
	}

	served := resp.GetComputes()
	if len(served) == 0 {
		t.Fatal("PreflightResponse.computes is empty, and a provider that names no compute can run nothing")
	}

	seen := make(map[string]bool, len(served))
	for _, compute := range served {
		if seen[compute] {
			t.Errorf("PreflightResponse.computes names %q twice, and the first entry is the default, so a repeat leaves the default ambiguous", compute)
		}
		seen[compute] = true
		if !provider.KnownCompute(compute) {
			t.Errorf("PreflightResponse.computes names %q, which is no compute the kit knows: %v", compute, provider.ComputeNames(provider.Computes()))
		}
	}

	declared := computesDeclared(t, suite)
	if want := provider.ComputeNames(declared); !slices.Equal(served, want) {
		t.Errorf("PreflightResponse.computes = %v, want %v — the wire answers what Computes() answers, order included", served, want)
	}
}

func computesDeclared(t *testing.T, suite Suite) []provider.Compute {
	t.Helper()

	if suite.Server.New == nil {
		t.Fatal("the suite carries no Spec.New, so nothing can read Computes() back off the provider the wire is serving")
	}
	p, err := suite.Server.New(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider to read Computes() from", err)
	}
	return p.Facts().Computes
}
