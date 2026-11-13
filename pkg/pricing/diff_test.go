package pricing_test

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func priced(id, fixed, usage string) *costv1.ResourceEstimate {
	return &costv1.ResourceEstimate{Resource: id, Status: costv1.ResourceEstimate_STATUS_PRICED, MonthlyFixed: fixed, MonthlyUsage: usage}
}

func TestADiffNamesWhatEachAddedRemovedAndRepricedResourceMoves(t *testing.T) {
	current := set(
		resource("kept", "p/e", "test_gadget", "eu-west-1", nil),
		resource("grown", "p/e", "test_gadget", "eu-west-1", nil),
		resource("gone", "p/e", "test_gadget", "eu-west-1", nil),
	)
	proposed := set(
		resource("kept", "p/e", "test_gadget", "eu-west-1", nil),
		resource("grown", "p/e", "test_gadget", "eu-west-1", nil),
		resource("new", "p/e", "test_gadget", "eu-west-1", nil),
	)
	before := &costv1.Estimate{MonthlyFixed: "30.00", MonthlyUsage: "1.00", Resources: []*costv1.ResourceEstimate{
		priced("kept", "10.00", "0.00"), priced("grown", "10.00", "1.00"), priced("gone", "10.00", "0.00"),
	}}
	after := &costv1.Estimate{MonthlyFixed: "35.00", MonthlyUsage: "1.50", Resources: []*costv1.ResourceEstimate{
		priced("kept", "10.00", "0.00"), priced("grown", "20.00", "1.50"), priced("new", "5.00", "0.00"),
	}}

	diff := pricing.Diff(current, proposed, before, after)

	if diff.GetMonthlyFixedDelta() != "5.00" || diff.GetMonthlyUsageDelta() != "0.50" || diff.GetCurrent() != before || diff.GetProposed() != after {
		t.Errorf("totals = %s + %s", diff.GetMonthlyFixedDelta(), diff.GetMonthlyUsageDelta())
	}
	want := []struct {
		id, fixed, usage string
		change           costv1.ResourceChange_Change
	}{
		{"grown", "10.00", "0.50", costv1.ResourceChange_CHANGE_MODIFIED},
		{"new", "5.00", "0.00", costv1.ResourceChange_CHANGE_ADDED},
		{"gone", "-10.00", "0.00", costv1.ResourceChange_CHANGE_REMOVED},
	}
	if len(diff.GetResources()) != len(want) {
		t.Fatalf("changes = %v, want %d with the unchanged resource left out", diff.GetResources(), len(want))
	}
	for i, w := range want {
		got := diff.GetResources()[i]
		if got.GetResource() != w.id || got.GetChange() != w.change || got.GetMonthlyFixedDelta() != w.fixed || got.GetMonthlyUsageDelta() != w.usage {
			t.Errorf("change %d = %v, want %+v", i, got, w)
		}
	}
}

func unpriced(id string, status costv1.ResourceEstimate_Status, components ...*costv1.CostComponent) *costv1.ResourceEstimate {
	return &costv1.ResourceEstimate{Resource: id, Status: status, Components: components}
}

func TestAResourceUnpricedOnEitherSideChangesByAnUnknownAmount(t *testing.T) {
	unknownFixed := &costv1.CostComponent{Name: "vCPU", PriceStatus: costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT}
	pricedUsage := &costv1.CostComponent{Name: "Requests", UsageBased: true, PriceStatus: costv1.CostComponent_PRICE_STATUS_PRICED, MonthlyCost: "1.00"}
	current := set(
		resource("lost", "p/e", "test_gadget", "eu-west-1", nil),
		resource("found", "p/e", "test_gadget", "eu-west-1", nil),
		resource("partial", "p/e", "test_gadget", "eu-west-1", nil),
		resource("still", "p/e", "test_gadget", "eu-west-1", nil),
		resource("gone", "p/e", "test_gadget", "eu-west-1", nil),
		resource("priced", "p/e", "test_gadget", "eu-west-1", nil),
	)
	proposed := set(
		resource("lost", "p/e", "test_gadget", "eu-west-1", nil),
		resource("found", "p/e", "test_gadget", "eu-west-1", nil),
		resource("partial", "p/e", "test_gadget", "eu-west-1", nil),
		resource("still", "p/e", "test_gadget", "eu-west-1", nil),
		resource("new", "p/e", "test_gadget", "eu-west-1", nil),
		resource("priced", "p/e", "test_gadget", "eu-west-1", nil),
	)
	before := &costv1.Estimate{MonthlyFixed: "71.00", MonthlyUsage: "1.00", Resources: []*costv1.ResourceEstimate{
		priced("lost", "60.00", "0.00"),
		unpriced("found", costv1.ResourceEstimate_STATUS_NO_PRICE, unknownFixed),
		priced("partial", "4.00", "1.00"),
		unpriced("still", costv1.ResourceEstimate_STATUS_NO_PRICE, unknownFixed),
		unpriced("gone", costv1.ResourceEstimate_STATUS_NO_PRICE, unknownFixed),
		priced("priced", "7.00", "0.00"),
	}}
	partial := priced("partial", "0.00", "2.00")
	partial.Components = []*costv1.CostComponent{unknownFixed, pricedUsage}
	after := &costv1.Estimate{MonthlyFixed: "29.00", MonthlyUsage: "2.00", Resources: []*costv1.ResourceEstimate{
		unpriced("lost", costv1.ResourceEstimate_STATUS_NO_PRICE, unknownFixed),
		priced("found", "20.00", "0.00"),
		partial,
		unpriced("still", costv1.ResourceEstimate_STATUS_NO_PRICE, unknownFixed),
		unpriced("new", costv1.ResourceEstimate_STATUS_UNSUPPORTED),
		priced("priced", "9.00", "0.00"),
	}}

	diff := pricing.Diff(current, proposed, before, after)

	want := map[string][2]string{
		"lost":    {"", "0.00"},
		"found":   {"", "0.00"},
		"partial": {"", "1.00"},
		"new":     {"", ""},
		"priced":  {"2.00", "0.00"},
		"gone":    {"", "0.00"},
	}
	got := map[string][2]string{}
	for _, change := range diff.GetResources() {
		got[change.GetResource()] = [2]string{change.GetMonthlyFixedDelta(), change.GetMonthlyUsageDelta()}
	}
	if !maps.Equal(got, want) {
		t.Errorf("changes = %v, want %v with the resource unpriced on both sides left out", got, want)
	}
	if diff.GetMonthlyFixedDelta() != "2.00" || diff.GetMonthlyUsageDelta() != "1.00" {
		t.Errorf("totals = %s + %s, want only the known changes, 2.00 + 1.00", diff.GetMonthlyFixedDelta(), diff.GetMonthlyUsageDelta())
	}
}
