package cost_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
)

func inventoriedSet(t *testing.T, tier environment.Tier) *costv1.ResourceSet {
	t.Helper()
	inventory, err := cloudflare.Inventory(pricing.EdgeSite{Slug: "shop", Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	tree := &pricing.Tree{}
	tree.AddEdge(pricing.EdgeScopes{
		Shared:      tree.Scope("", pricing.ScopeShared, string(tier)),
		Environment: tree.Scope("", pricing.ScopeEnvironment, "prod"),
	}, inventory)
	set, err := tree.Set("ocel")
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func priced(t *testing.T, set *costv1.ResourceSet, profile costv1.Profile) *costv1.Estimate {
	t.Helper()
	card, err := cost.Card()
	if err != nil {
		t.Fatal(err)
	}
	est, err := pricing.Estimate(card, cost.Table, &costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: profile}})
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

func TestTheEdgeInventoriesItsPlanStoreAndEntry(t *testing.T) {
	t.Parallel()

	set := inventoriedSet(t, environment.TierProduction)
	counts := map[string]int{}
	for _, r := range set.GetResources() {
		counts[r.GetType()]++
		if r.GetVendor() != cost.Vendor {
			t.Errorf("%s is %s", r.GetId(), r.GetVendor())
		}
	}
	if counts[cost.TypeAccountSubscription] != 1 || counts[cost.TypeWorkersScript] != 2 || len(counts) != 2 {
		t.Errorf("counts = %v, want the plan and the store and entry workers, and no bucket", counts)
	}
	preview := inventoriedSet(t, environment.TierPreview)
	if n := len(preview.GetResources()); n != len(set.GetResources())+1 {
		t.Errorf("a preview tier inventories %d resources, want the production set plus the shared preview entry worker", n)
	}
}

func TestEveryEntryWorkerIsPricedForItsPurgeQueue(t *testing.T) {
	t.Parallel()

	set := inventoriedSet(t, environment.TierPreview)
	est := priced(t, set, costv1.Profile_PROFILE_MODERATE)

	for _, scope := range []string{"environment:prod", "shared:preview"} {
		var entry *costv1.Resource
		for _, resource := range set.GetResources() {
			if resource.GetType() == cost.TypeWorkersScript && resource.GetScope() == scope && strings.Contains(resource.GetName(), "entry") {
				entry = resource
			}
		}
		if entry == nil {
			t.Fatalf("no entry worker under %s", scope)
		}
		var components []string
		for _, r := range est.GetResources() {
			if r.GetResource() != entry.GetId() {
				continue
			}
			for _, c := range r.GetComponents() {
				components = append(components, c.GetName())
			}
		}
		if !slices.Contains(components, "Durable Object requests") {
			t.Errorf("%s prices %v, want the Durable Object requests of its PurgeQueue", entry.GetName(), components)
		}
	}
}

func TestAModerateMonthStaysInsideThePaidPlansAllowances(t *testing.T) {
	t.Parallel()

	set := inventoriedSet(t, environment.TierProduction)
	est := priced(t, set, costv1.Profile_PROFILE_MODERATE)

	plan := estimateOfType(t, est, set, cost.TypeAccountSubscription, "shared:production")
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

	set := inventoriedSet(t, environment.TierProduction)
	est := priced(t, set, costv1.Profile_PROFILE_HEAVY)

	entry := estimateOfType(t, est, set, cost.TypeWorkersScript, "environment:prod")
	var requests *costv1.CostComponent
	for _, c := range entry.GetComponents() {
		if c.GetName() == "Requests" {
			requests = c
		}
	}
	if requests.GetMonthlyCost() != "3.00" || requests.GetMonthlyQuantity() != "10000000" {
		t.Errorf("entry's 10M requests = %v, want 3.00: the shared workers spent the plan's included 10M first", requests)
	}
}

func TestTheIncludedRequestsAreSpentOnceAcrossEveryWorker(t *testing.T) {
	t.Parallel()

	set := inventoriedSet(t, environment.TierProduction)
	est := priced(t, set, costv1.Profile_PROFILE_HEAVY)

	var billed int
	var total decimal.Decimal
	for _, r := range est.GetResources() {
		for _, c := range r.GetComponents() {
			if c.GetName() != "Requests" {
				continue
			}
			cost, _ := decimal.NewFromString(c.GetMonthlyCost())
			total = total.Add(cost)
			if cost.IsPositive() {
				billed++
			}
		}
	}
	if billed != 1 || total.String() != "3" {
		t.Errorf("two workers at 10M requests each billed %d workers for %s; want the plan's 10M spent once, then 10M at 0.30 a million", billed, total)
	}
}
