package conformance

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProviderThatProvisionsNoKVStorePassesByRefusingOneAsUnsupported(t *testing.T) {
	facts := provider.Facts{Vendor: "nowhere", Bindings: []provider.BindingType{provider.BindingPostgres}}
	if found := kvStoreFaults(facts); len(found) != 0 {
		t.Errorf("kvStoreFaults() = %v, want none from a provider that refuses kv stores", found)
	}
}

func TestAProviderThatProvisionsKVStoresIsNotHeldToTheRefusal(t *testing.T) {
	facts := provider.Facts{Vendor: "nowhere", Bindings: []provider.BindingType{provider.BindingKV}}
	if found := kvStoreFaults(facts); len(found) != 0 {
		t.Errorf("kvStoreFaults() = %v, want none from a provider that provisions kv stores", found)
	}
}
