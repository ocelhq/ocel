package cloudflare_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/cloudflare"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func worker(id, scope string, props map[string]any) *costv1.Resource {
	props["main_module"] = "worker.js"
	properties, _ := structpb.NewStruct(props)
	return &costv1.Resource{Id: id, Scope: scope, Vendor: "cloudflare", Type: "cloudflare_workers_script", Name: id, Properties: properties}
}

func defining(classes ...any) map[string]any {
	return map[string]any{"migrations": map[string]any{"new_sqlite_classes": classes}}
}

func binding(class string) map[string]any {
	return map[string]any{"type": "durable_object_namespace", "name": class, "class_name": class}
}

func edge() *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{
			{Id: "shared:production", Kind: "shared", Name: "production"},
			{Id: "environment:prod", Kind: "environment", Name: "prod"},
		},
		Resources: []*costv1.Resource{
			{Id: "plan", Scope: "shared:production", Vendor: "cloudflare", Type: "cloudflare_account_subscription", Name: "plan"},
			worker("store", "shared:production", defining("Store")),
			worker("entry", "environment:prod", defining("PurgeQueue")),
		},
	}
}

func static(t *testing.T) *pricingtest.Card {
	t.Helper()
	raw, err := os.ReadFile("../ingest/static/cloudflare.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pricingtest.NewCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func estimate(t *testing.T, set *costv1.ResourceSet, profile costv1.Profile) *costv1.Estimate {
	t.Helper()
	return estimateWith(t, static(t), set, profile)
}

func estimateWith(t *testing.T, catalog *pricingtest.Card, set *costv1.ResourceSet, profile costv1.Profile) *costv1.Estimate {
	t.Helper()
	est, err := pricing.Estimate(context.Background(), catalog, pricing.Combine(cloudflare.Rules), set, &costv1.Usage{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func byID(est *costv1.Estimate, id string) *costv1.ResourceEstimate {
	for _, r := range est.GetResources() {
		if r.GetResource() == id {
			return r
		}
	}
	return nil
}

func TestEveryCloudflareComponentIsPricedByTheStaticCatalog(t *testing.T) {
	est := estimate(t, edge(), costv1.Profile_PROFILE_MODERATE)

	for _, r := range est.GetResources() {
		for _, c := range r.GetComponents() {
			if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
				t.Errorf("%s %s = %v %s", r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
			}
		}
	}
}

func TestEveryProductTheRulesAskForIsOneTheManifestIngests(t *testing.T) {
	catalog := static(t)
	estimateWith(t, catalog, edge(), costv1.Profile_PROFILE_MODERATE)

	if missed := catalog.MissedBy(cloudflare.Rules.Ingest); len(missed) > 0 {
		t.Errorf("the manifest misses %v", missed)
	}
}

func TestAModerateMonthStaysInsideThePaidPlansAllowances(t *testing.T) {
	est := estimate(t, edge(), costv1.Profile_PROFILE_MODERATE)

	if plan := byID(est, "plan"); plan.GetMonthlyFixed() != "5.00" {
		t.Errorf("plan = %s, want the 5.00 the paid plan costs", plan.GetMonthlyFixed())
	}
	if est.GetMonthlyFixed() != "5.00" || est.GetMonthlyUsage() != "0.00" {
		t.Errorf("totals = %s + %s, want 5.00 fixed and nothing beyond the plan's included usage", est.GetMonthlyFixed(), est.GetMonthlyUsage())
	}
	if cov := est.GetCoverage(); cov.GetUnsupported() != 0 || cov.GetNoPrice() != 0 {
		t.Errorf("coverage = %v", cov)
	}
}

func TestAHeavyMonthBillsRequestsPastTheAllowanceOnce(t *testing.T) {
	est := estimate(t, edge(), costv1.Profile_PROFILE_HEAVY)

	var billed []string
	total := decimal.Zero
	for _, r := range est.GetResources() {
		for _, c := range r.GetComponents() {
			if c.GetName() != "Requests" {
				continue
			}
			cost := decimal.RequireFromString(c.GetMonthlyCost())
			total = total.Add(cost)
			if cost.IsPositive() {
				billed = append(billed, r.GetResource())
			}
		}
	}
	if strings.Join(billed, ",") != "entry" || total.String() != "3" {
		t.Errorf("billed %v for %s; want the plan's 10M spent by the store, then entry's 10M at 0.30 a million", billed, total)
	}
}

func TestAWorkerWithoutDurableObjectsPaysOnlyForRequestsAndCPU(t *testing.T) {
	set := edge()
	set.Resources = []*costv1.Resource{worker("plain", "environment:prod", map[string]any{})}
	est := estimate(t, set, costv1.Profile_PROFILE_MODERATE)

	var names []string
	for _, c := range byID(est, "plain").GetComponents() {
		names = append(names, c.GetName())
	}
	if strings.Join(names, ",") != "Requests,CPU time" {
		t.Errorf("components = %v", names)
	}
}

func TestDurableObjectsArePricedOnceOnTheScriptThatDefinesTheirClasses(t *testing.T) {
	definedAndBound := defining("Store")
	definedAndBound["bindings"] = []any{binding("Store")}
	elsewhere := binding("Queue")
	elsewhere["script_name"] = "entry"
	set := edge()
	set.Resources = []*costv1.Resource{
		worker("defined-and-bound", "environment:prod", definedAndBound),
		worker("defined", "environment:prod", map[string]any{"migrations": map[string]any{"steps": []any{map[string]any{"new_classes": []any{"PurgeQueue"}}}}}),
		worker("bound", "environment:prod", map[string]any{"bindings": []any{elsewhere}}),
		worker("renamed", "environment:prod", map[string]any{"migrations": map[string]any{"old_tag": "v1", "new_tag": "v2",
			"renamed_classes": []any{map[string]any{"from": "OldStore", "to": "Store"}}}}),
		worker("transferred", "environment:prod", map[string]any{"migrations": map[string]any{"steps": []any{
			map[string]any{"transferred_classes": []any{map[string]any{"from": "Store", "from_script": "old", "to": "Store"}}},
		}}}),
		worker("self-bound", "environment:prod", map[string]any{"bindings": []any{binding("Store"), elsewhere}}),
	}
	est := estimate(t, set, costv1.Profile_PROFILE_MODERATE)

	objectComponents := func(id string) int {
		count := 0
		for _, c := range byID(est, id).GetComponents() {
			if c.GetName() == "Durable Object requests" {
				count++
			}
		}
		return count
	}
	for id, want := range map[string]int{"defined-and-bound": 1, "defined": 1, "bound": 0, "renamed": 1, "transferred": 1, "self-bound": 1} {
		if got := objectComponents(id); got != want {
			t.Errorf("%s has %d Durable Object request components, want %d", id, got, want)
		}
	}
}

func TestAnUnknownBindingLeavesDurableObjectsUnpricedOnlyWhereItCouldBindTheScriptsOwnClass(t *testing.T) {
	keyed := worker("keyed", "environment:prod", map[string]any{"bindings": []any{map[string]any{"type": "kv_namespace", "name": "KV"}}})
	keyed.Unknown = []string{"bindings.0.namespace_id"}
	blurred := worker("blurred", "environment:prod", map[string]any{})
	blurred.Unknown = []string{"bindings"}
	set := edge()
	set.Resources = []*costv1.Resource{keyed, blurred}
	est := estimate(t, set, costv1.Profile_PROFILE_MODERATE)

	for _, c := range byID(est, "keyed").GetComponents() {
		if pricing.Unpriced(c) || strings.HasPrefix(c.GetName(), "Durable Object") {
			t.Errorf("keyed %s = %v, want only its priced requests and CPU time", c.GetName(), c.GetPriceStatus())
		}
	}
	for _, c := range byID(est, "blurred").GetComponents() {
		durable := strings.HasPrefix(c.GetName(), "Durable Object") || strings.HasPrefix(c.GetName(), "Rows") || c.GetName() == "Stored data"
		if durable != (c.GetPriceStatus() == costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT) {
			t.Errorf("blurred %s = %v, want only Durable Object components unpriced", c.GetName(), c.GetPriceStatus())
		}
	}
}

func TestAScriptWhoseMigrationsAreUnknownLeavesDurableObjectsUnpriced(t *testing.T) {
	blurred := worker("blurred", "environment:prod", map[string]any{})
	blurred.Unknown = []string{"migrations"}
	set := edge()
	set.Resources = []*costv1.Resource{blurred}
	est := estimate(t, set, costv1.Profile_PROFILE_MODERATE)

	for _, c := range byID(est, "blurred").GetComponents() {
		durable := strings.HasPrefix(c.GetName(), "Durable Object") || strings.HasPrefix(c.GetName(), "Rows") || c.GetName() == "Stored data"
		if durable != (c.GetPriceStatus() == costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT) {
			t.Errorf("%s = %v, want only Durable Object components unpriced", c.GetName(), c.GetPriceStatus())
		}
	}
}
