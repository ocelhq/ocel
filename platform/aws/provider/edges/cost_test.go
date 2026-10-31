package edges

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

func TestEveryEdgeThisProviderFrontsWithInventoriesWhatItProvisions(t *testing.T) {
	t.Parallel()

	site := pricing.EdgeSite{Slug: "shop", Tier: environment.TierProduction, Region: "us-east-1"}
	for _, kind := range SupportedEdges() {
		inventory, err := Inventory(kind, "ocel", site)
		if err != nil {
			t.Fatalf("Inventory(%s) = %v", kind, err)
		}
		if inventory.Vendor == "" || len(inventory.Shared)+len(inventory.Environment)+len(inventory.Apps) == 0 {
			t.Errorf("Inventory(%s) = %+v, want what the edge provisions for the site, billed to its vendor", kind, inventory)
		}
	}
}

func TestAnEdgeThisProviderCannotFrontWithInventoriesNothing(t *testing.T) {
	t.Parallel()

	inventory, err := Inventory("relay", "ocel", pricing.EdgeSite{Slug: "shop", Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("Inventory(relay) = %v", err)
	}
	if inventory.Vendor != "" || len(inventory.Shared)+len(inventory.Environment)+len(inventory.Apps) != 0 {
		t.Errorf("Inventory(relay) = %+v, want nothing inventoried for an edge this provider does not front with", inventory)
	}
}
