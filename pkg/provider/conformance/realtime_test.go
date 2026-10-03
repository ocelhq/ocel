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

func TestEveryRealtimeTheStacksSuiteProvisionsCarriesTheChannelsADeployHandsIt(t *testing.T) {
	for _, resource := range declared([]provider.BindingType{provider.BindingRealtime, provider.BindingKV}) {
		if resource.Type == provider.BindingRealtime && (resource.Realtime == nil || len(resource.Realtime.Channels) == 0) {
			t.Errorf("declared() handed realtime %s %+v, and a deploy hands every realtime its channels: a provider refusing it fails conformance for a resource no deploy sends", resource.Name, resource.Realtime)
		}
		if resource.Type != provider.BindingRealtime && resource.Realtime != nil {
			t.Errorf("declared() handed %s %s a realtime config, and only realtime takes one", resource.Type, resource.Name)
		}
	}
}
