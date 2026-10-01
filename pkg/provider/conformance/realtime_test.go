package conformance

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProviderThatProvisionsNoRealtimePassesByRefusingItAsUnsupported(t *testing.T) {
	facts := provider.Facts{Vendor: "nowhere", Bindings: []provider.BindingType{provider.BindingPostgres}}
	if found := realtimeFaults(facts); len(found) != 0 {
		t.Errorf("realtimeFaults() = %v, want none from a provider that refuses realtime", found)
	}
}

func TestAProviderThatProvisionsRealtimeIsNotHeldToTheRefusal(t *testing.T) {
	facts := provider.Facts{Vendor: "nowhere", Bindings: []provider.BindingType{provider.BindingRealtime}}
	if found := realtimeFaults(facts); len(found) != 0 {
		t.Errorf("realtimeFaults() = %v, want none from a provider that provisions realtime", found)
	}
}
