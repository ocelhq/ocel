package component

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAComponentDeclaresOnlyTheUnboundResourcesOfItsOwnTypes(t *testing.T) {
	t.Parallel()

	realtime := Component{Types: []provider.BindingType{provider.BindingRealtime}}
	resources := []provider.Resource{
		{Name: "realtime--app", Declared: "app", Type: provider.BindingRealtime},
		{Name: "realtime--shared", Declared: "shared", Type: provider.BindingRealtime, Binding: "elsewhere"},
		{Name: "kv--cache", Declared: "cache", Type: provider.BindingKV},
	}
	got := realtime.Declared(resources)
	if len(got) != 1 || got[0].Name != "realtime--app" {
		t.Errorf("Declared() = %v, want realtime--app alone: a bound resource is someone else's, and a kv is no realtime", got)
	}
}

func TestARegistryRefusesAResourceNoComponentServesNamingWhatItServes(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(
		Component{Types: []provider.BindingType{provider.BindingPostgres}},
		Component{Types: []provider.BindingType{provider.BindingBucket}},
		Component{Types: []provider.BindingType{provider.BindingKV}},
	)
	err := registry.RefuseUnserved([]provider.Resource{{Name: "mq", Type: "queue"}})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "provisions no queue") {
		t.Fatalf("RefuseUnserved() = %v, want the unserved type refused as invalid", err)
	}
	if !strings.Contains(err.Error(), "postgres, bucket and kv") {
		t.Errorf("RefuseUnserved() = %v, want it to list what this provider provisions", err)
	}
	if err := registry.RefuseUnserved([]provider.Resource{{Name: "mq", Type: "queue", Binding: "elsewhere"}}); err != nil {
		t.Errorf("RefuseUnserved() = %v, want a bound resource of any type accepted: nothing is provisioned for it", err)
	}
	if got := registry.Types(); !slices.Equal(got, []provider.BindingType{provider.BindingPostgres, provider.BindingBucket, provider.BindingKV}) {
		t.Errorf("Types() = %v, want each component's types in registry order", got)
	}
}
