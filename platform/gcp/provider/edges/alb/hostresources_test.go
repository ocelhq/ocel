package alb

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func TestHostResourcesNameTheEntryNEGAndBackendServiceEachBoundHostAdded(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	state := edge.StackState{Slug: "shop", Tier: tier, Private: edge.Own(edgeRecord{Hosts: map[string]Host{
		"shop.example.com": {
			App: "web", Certificate: "projects/acme-prod/locations/global/certificates/ocel-shop-example-com",
			Service: "ocel-shop-prod-web-1a2b3c", Backend: must(albresources.BackendName("ocel", "shop", tier, "shop.example.com")),
		},
		"pending.example.com": {App: "web", Backend: must(albresources.BackendName("ocel", "shop", tier, "pending.example.com"))},
	}})}

	listed, err := ReadHostResources("ocel", state)
	if err != nil {
		t.Fatal(err)
	}

	want := []HostResources{
		{Hostname: "pending.example.com"},
		{
			Hostname:             "shop.example.com",
			CertificateMapEntry:  must(albresources.EntryName("ocel", "shop", tier, "shop.example.com")),
			NetworkEndpointGroup: must(albresources.NegName("ocel", "shop", tier, "shop.example.com")),
			BackendService:       must(albresources.BackendName("ocel", "shop", tier, "shop.example.com")),
		},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ReadHostResources() = %+v, want %+v: a host with no certificate holds no map entry, and one serving no service holds no NEG or backend service", listed, want)
	}
}

func TestHostResourcesAreNoneForAnEdgeStackThatBindsNoHost(t *testing.T) {
	t.Parallel()

	listed, err := ReadHostResources("ocel", edge.StackState{Slug: "shop", Tier: environment.TierPreview})
	if err != nil || len(listed) != 0 {
		t.Errorf("ReadHostResources() = %+v, %v, want nothing", listed, err)
	}
}
