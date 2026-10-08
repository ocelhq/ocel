package build

import (
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestReadHostCarriesTheFactsABuildReads(t *testing.T) {
	t.Parallel()

	got := ReadHost(&contractv1.ProviderFacts{MaxFunctionBytes: 1024, NextRefreshesByRequest: true})

	if want := (Host{MaxFunctionBytes: 1024, NextRefreshesByRequest: true}); got != want {
		t.Errorf("ReadHost() = %+v, want %+v", got, want)
	}
}
