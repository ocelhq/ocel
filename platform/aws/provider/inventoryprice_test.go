package aws_test

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
)

var fixtureCatalogs = []string{
	"../../../pkg/pricing/aws/testdata/catalog.json",
	"../../../pkg/pricing/ingest/static/cloudflare.json",
}

func TestEveryInventoryGoldenPricesEveryComponentAgainstTheFixtureCatalogs(t *testing.T) {
	var catalogs [][]byte
	for _, path := range fixtureCatalogs {
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
		est, err := pricing.Estimate(context.Background(), card, ruleset.Table(), set, nil)
		if err != nil {
			t.Fatalf("%s: Estimate() = %v", path, err)
		}
		for _, r := range est.GetResources() {
			if status := r.GetStatus(); status != costv1.ResourceEstimate_STATUS_PRICED && status != costv1.ResourceEstimate_STATUS_FREE {
				t.Errorf("%s: %s is %v", filepath.Base(path), r.GetResource(), status)
			}
			for _, c := range r.GetComponents() {
				if pricing.Unpriced(c) {
					t.Errorf("%s: %s %s is %v: %s", filepath.Base(path), r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
				}
			}
		}
	}
}
