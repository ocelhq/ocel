package deploy

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func TestEveryListedResourcePricesEveryComponentAgainstTheFixtureCatalog(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../../pkg/pricing/aws/testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	card, err := pricingtest.NewCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	est, err := pricing.Estimate(context.Background(), card, ruleset.Table(), items(t, nil, inventoryRequest(t)), nil)
	if err != nil {
		t.Fatalf("Estimate() = %v", err)
	}
	for _, r := range est.GetResources() {
		if status := r.GetStatus(); status != costv1.ResourceEstimate_STATUS_PRICED && status != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s is %v", r.GetResource(), status)
		}
		for _, c := range r.GetComponents() {
			if pricing.Unpriced(c) {
				t.Errorf("%s %s is %v: %s", r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
			}
		}
	}
}
