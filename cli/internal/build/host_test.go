package build

import (
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestReadHostSaysWhetherTheProviderShipsANextServerRuntime(t *testing.T) {
	t.Parallel()

	if !ReadHost(&contractv1.ProviderFacts{ShipsNextServerRuntime: true}).ShipsNextServerRuntime {
		t.Error("ReadHost() ShipsNextServerRuntime = false, want true for a provider that ships one")
	}
	if ReadHost(&contractv1.ProviderFacts{}).ShipsNextServerRuntime {
		t.Error("ReadHost() ShipsNextServerRuntime = true, want false for a provider that ships none")
	}
}
