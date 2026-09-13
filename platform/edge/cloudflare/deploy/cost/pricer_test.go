package cost_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func shapedSet(t *testing.T, class edge.Class) *costv1.ResourceSet {
	t.Helper()
	shape, err := cloudflare.ShapeEdge("ocel", class)
	if err != nil {
		t.Fatal(err)
	}
	tree := &costkit.Tree{}
	shared := tree.Scope("", "shared", string(class))
	environment := tree.Scope("", "environment", "prod")
	for _, item := range shape.Shared {
		tree.Add(shared, cloudflare.Vendor, item.Type, item.Name, "", item.Properties)
	}
	for _, item := range shape.Environment {
		tree.Add(environment, cloudflare.Vendor, item.Type, item.Name, "", item.Properties)
	}
	return tree.Set("ocel")
}

func priced(t *testing.T, set *costv1.ResourceSet, profile string) *costv1.Estimate {
	t.Helper()
	card, err := cost.Card()
	if err != nil {
		t.Fatal(err)
	}
	est, err := costkit.Estimate(card, cost.Table, &costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: profile}})
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func estimateOfType(t *testing.T, est *costv1.Estimate, set *costv1.ResourceSet, typ, scope string) *costv1.ResourceEstimate {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() != typ || resource.GetScope() != scope {
			continue
		}
		for _, r := range est.GetResources() {
			if r.GetResource() == resource.GetId() {
				return r
			}
		}
	}
	t.Fatalf("no %s under %s", typ, scope)
	return nil
}

func TestTheEdgeShapesItsPlanStoreWriterCacheAndEntry(t *testing.T) {
	t.Parallel()

	set := shapedSet(t, edge.ClassProduction)
	counts := map[string]int{}
	for _, r := range set.GetResources() {
		counts[r.GetType()]++
		if r.GetVendor() != cloudflare.Vendor {
			t.Errorf("%s is %s", r.GetId(), r.GetVendor())
		}
	}
	if counts[cloudflare.TypeAccountSubscription] != 1 || counts[cloudflare.TypeR2Bucket] != 1 || counts[cloudflare.TypeWorkersScript] != 3 {
		t.Errorf("counts = %v, want the plan, the cache bucket, and the store, writer and entry workers", counts)
	}
	preview := shapedSet(t, edge.ClassPreview)
	if n := len(preview.GetResources()); n != len(set.GetResources())+1 {
		t.Errorf("a preview class shapes %d resources, want the production set plus the shared preview entry worker", n)
	}
}

func TestAModerateMonthStaysInsideThePaidPlansAllowances(t *testing.T) {
	t.Parallel()

	set := shapedSet(t, edge.ClassProduction)
	est := priced(t, set, "moderate")

	plan := estimateOfType(t, est, set, cloudflare.TypeAccountSubscription, "shared:production")
	if plan.GetMonthlyFixed() != "5.00" {
		t.Errorf("plan = %s, want the 5.00 the paid plan costs", plan.GetMonthlyFixed())
	}
	if est.GetMonthlyFixed() != "5.00" || est.GetMonthlyUsage() != "0.00" {
		t.Errorf("totals = %s + %s, want 5.00 fixed and nothing beyond the plan's included usage", est.GetMonthlyFixed(), est.GetMonthlyUsage())
	}
	if cov := est.GetCoverage(); cov.GetUnsupported() != 0 || cov.GetNoPrice() != 0 {
		t.Errorf("coverage = %v", cov)
	}
}

func TestAHeavyMonthBillsRequestsPastTheAllowance(t *testing.T) {
	t.Parallel()

	set := shapedSet(t, edge.ClassProduction)
	est := priced(t, set, "heavy")

	entry := estimateOfType(t, est, set, cloudflare.TypeWorkersScript, "environment:prod")
	var requests *costv1.CostComponent
	for _, c := range entry.GetComponents() {
		if c.GetName() == "Requests" {
			requests = c
		}
	}
	if requests.GetMonthlyCost() != "0.00" || requests.GetMonthlyQuantity() != "10000000" {
		t.Errorf("10M requests = %v, want the plan's included 10M and nothing billed", requests)
	}
	if cost := estimateOfType(t, est, set, cloudflare.TypeR2Bucket, "shared:production").GetMonthlyUsage(); cost != "1.35" {
		t.Errorf("r2 at 100 GB = %s, want 1.35 (90 GB past the 10 included at 0.015)", cost)
	}
}
