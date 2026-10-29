package pricing_test

import (
	"context"
	"testing"

	"buf.build/go/protovalidate"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func everyStatus() *costv1.ResourceSet {
	return set(
		resource("w", "p/e/a", "test_widget", "eu-west-1", map[string]any{}),
		resource("t", "p/e", "test_trinket", "eu-west-1", map[string]any{}),
		resource("x", "p/e", "test_unknown_thing", "eu-west-1", map[string]any{}),
		resource("u", "p/e", "test_gadget", "eu-west-1", map[string]any{}, "size"),
		resource("m", "p/e", "test_widget", "us-east-1", map[string]any{}),
		resource("a", "p/e", "test_twin", "eu-west-1", map[string]any{}),
	)
}

func TestEveryEstimateAndDiffKeepsTheRulesOfItsMessages(t *testing.T) {
	usages := []*costv1.Usage{nil, {}}
	for _, profile := range pricing.Profiles() {
		usages = append(usages, &costv1.Usage{Profile: profile})
	}
	for _, catalog := range []pricing.Catalog{loaded(t), offline{}} {
		for _, usage := range usages {
			before := estimateWith(t, catalog, widgets, set(), usage)
			after := estimateWith(t, catalog, widgets, everyStatus(), usage)
			if err := protovalidate.Validate(after); err != nil {
				t.Errorf("estimate on %v breaks its rules: %v", usage.GetProfile(), err)
			}
			if err := protovalidate.Validate(pricing.Diff(set(), everyStatus(), before, after)); err != nil {
				t.Errorf("diff on %v breaks its rules: %v", usage.GetProfile(), err)
			}
		}
	}
}

func TestAnEstimateMissingItsProfileOrStatusesBreaksItsRules(t *testing.T) {
	for name, est := range map[string]*costv1.Estimate{
		"profile":      {},
		"status":       {Profile: pricing.DefaultProfile, Resources: []*costv1.ResourceEstimate{{Resource: "w"}}},
		"price status": {Profile: pricing.DefaultProfile, Resources: []*costv1.ResourceEstimate{{Resource: "w", Status: costv1.ResourceEstimate_STATUS_NO_PRICE, Components: []*costv1.CostComponent{{Name: "Base"}}}}},
	} {
		if err := protovalidate.Validate(est); err == nil {
			t.Errorf("an estimate with no %s keeps its rules, want it refused", name)
		}
	}
	if err := protovalidate.Validate(&costv1.ResourceChange{Resource: "w"}); err == nil {
		t.Error("a resource change with no change keeps its rules, want it refused")
	}
}

func TestAnUndefinedProfileIsRefused(t *testing.T) {
	est, err := pricing.Estimate(context.Background(), loaded(t), widgets, set(), &costv1.Usage{Profile: costv1.Profile(99)})

	if err == nil || est != nil {
		t.Fatalf("Estimate() = %v, %v; want an error and no estimate", est, err)
	}
}
