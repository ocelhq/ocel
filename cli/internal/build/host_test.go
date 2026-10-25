package build

import (
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestReadHostCarriesTheFactsABuildReads(t *testing.T) {
	t.Parallel()

	got := ReadHost(&contractv1.ProviderFacts{
		Computes:         []string{"serverless", "container"},
		MaxFunctionBytes: 1024,
		Edge: &contractv1.EdgeFacts{
			RunsCode:           true,
			CacheTagLimits:     &contractv1.CacheTagLimits{TagBytes: 64, Tags: 8, HeaderBytes: 256},
			DispatchedComputes: []string{"serverless", "container"},
		},
	})

	want := Host{
		RunsServerless:           true,
		MaxFunctionBytes:         1024,
		EdgeRunsCode:             true,
		EdgeDispatchesContainers: true,
		EdgeCacheTagLimits:       CacheTagLimits{TagBytes: 64, Tags: 8, HeaderBytes: 256},
	}
	if got != want {
		t.Errorf("ReadHost() = %+v, want %+v", got, want)
	}
}
