package cost_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

func set() *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{
			{Id: "p/google_storage_bucket:uploads", Scope: "p", Vendor: cost.Vendor, Type: "google_storage_bucket", Name: "uploads", Region: "us-east-1"},
		},
	}
}

func TestThePartsPriceWhatPriceDoes(t *testing.T) {
	card, err := cost.Card()
	if err != nil {
		t.Fatal(err)
	}
	if cost.Vendor != "gcp" {
		t.Errorf("Vendor = %q", cost.Vendor)
	}
	if _, prices := cost.Table["google_storage_bucket"]; !prices {
		t.Error("the table prices no bucket")
	}
	if len(cost.Notes) == 0 {
		t.Error("the pricer publishes no provenance notes")
	}

	whole, err := cost.Price(&costv1.PriceRequest{Resources: set()})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := costkit.Estimate(card, cost.Table, &costv1.PriceRequest{Resources: set()})
	if err != nil {
		t.Fatal(err)
	}
	if whole.GetMonthlyUsage() != parts.GetMonthlyUsage() || whole.GetRatesVersion() != parts.GetRatesVersion() {
		t.Errorf("Price() = %s at %s, the parts price %s at %s", whole.GetMonthlyUsage(), whole.GetRatesVersion(), parts.GetMonthlyUsage(), parts.GetRatesVersion())
	}
	for _, note := range cost.Notes {
		if !slices.Contains(whole.GetNotes(), note) {
			t.Errorf("Price() drops the note %q", note)
		}
	}
}
