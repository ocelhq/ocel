package gcp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func fixtureCard(t *testing.T) *pricingtest.Card {
	t.Helper()
	var catalogs [][]byte
	for _, path := range []string{"../../../pkg/pricing/gcp/testdata/catalog.json", "../../../pkg/pricing/ingest/static/cloudflare.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		catalogs = append(catalogs, raw)
	}
	card, err := pricingtest.NewCard(catalogs...)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func refuseUnpriced(t *testing.T, card *pricingtest.Card, name string, set *costv1.ResourceSet) {
	t.Helper()
	est, err := pricing.Estimate(context.Background(), card, ruleset.Table(), set, nil)
	if err != nil {
		t.Fatalf("%s: Estimate() = %v", name, err)
	}
	for _, r := range est.GetResources() {
		if status := r.GetStatus(); status != costv1.ResourceEstimate_STATUS_PRICED && status != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s: %s is %v", name, r.GetResource(), status)
		}
		for _, c := range r.GetComponents() {
			if pricing.Unpriced(c) {
				t.Errorf("%s: %s %s is %v: %s", name, r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
			}
		}
	}
}

func TestEveryInventoryGoldenPricesEveryComponentAgainstTheFixtureCatalogs(t *testing.T) {
	card := fixtureCard(t)
	goldens, err := filepath.Glob(filepath.Join("testdata", "inventory_*.golden.json"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("no inventory goldens: %v", err)
	}
	for _, path := range goldens {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		set := &costv1.ResourceSet{}
		if err := protojson.Unmarshal(raw, set); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		refuseUnpriced(t, card, filepath.Base(path), set)
	}
}

func TestEveryDeclaredResourcePricesEveryComponentAgainstTheFixtureCatalogs(t *testing.T) {
	card := fixtureCard(t)
	refuseUnpriced(t, card, "database", inventoryOfADatabase(t))
	refuseUnpriced(t, card, "store", inventoryOfAStore(t, productionEnvironment, "256mb"))
	for name, manifest := range map[string]*contractv1.Manifest{"topics": topicsManifest(), "realtime": realtimeManifest()} {
		set, err := inventoryServed(t).Inventory(context.Background(), &contractv1.InventoryRequest{
			Manifest:    manifest,
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		})
		if err != nil {
			t.Fatalf("Inventory() = %v", err)
		}
		refuseUnpriced(t, card, name, set)
	}
}
