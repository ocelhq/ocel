package pricing_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

const card = `{
  "format": 1,
  "vendor": "test",
  "version": "2026-09-01",
  "currency": "USD",
  "products": [
    {"service": "Widgets", "sku": "HOURS", "region": "eu-west-1", "attributes": {"usagetype": "hours"}, "purchaseOption": "on_demand",
     "unit": "hour", "tiers": [{"start": "0", "price": "0.10"}], "source": "https://example.test/hours", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "REQUESTS", "region": "eu-west-1", "attributes": {"usagetype": "requests"}, "purchaseOption": "on_demand",
     "unit": "1M requests", "tiers": [{"start": "0", "price": "0"}, {"start": "1", "price": "0.20"}], "source": "https://example.test/requests", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "STORAGE", "region": "eu-west-1", "attributes": {"usagetype": "storage"}, "purchaseOption": "on_demand",
     "unit": "GB-month", "tiers": [{"start": "0", "price": "0.05"}], "source": "https://example.test/storage", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "READS", "region": "eu-west-1", "attributes": {"usagetype": "reads"}, "purchaseOption": "on_demand",
     "unit": "reads", "tiers": [{"start": "0", "price": "0"}, {"start": "100", "price": "0.01"}], "source": "https://example.test/reads", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "PREMIUM", "region": "eu-west-1", "attributes": {"usagetype": "premium"}, "purchaseOption": "on_demand",
     "unit": "hour", "tiers": [{"start": "0", "price": "1"}], "source": "https://example.test/premium", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "TWIN-B", "region": "eu-west-1", "attributes": {"usagetype": "twin", "version": "Current"}, "purchaseOption": "on_demand",
     "unit": "hour", "tiers": [{"start": "0", "price": "1"}], "source": "https://example.test/twin", "verified": "2026-09-01"},
    {"service": "Widgets", "sku": "TWIN-A", "region": "eu-west-1", "attributes": {"usagetype": "twin", "version": "Legacy"}, "purchaseOption": "on_demand",
     "unit": "hour", "tiers": [{"start": "0", "price": "1"}], "source": "https://example.test/twin", "verified": "2026-09-01"},
    {"service": "Gizmos", "sku": "EACH", "region": "", "attributes": {"usagetype": "each"}, "purchaseOption": "on_demand",
     "unit": "each", "tiers": [{"start": "0", "price": "2"}], "source": "https://example.test/each", "verified": "2026-09-01"}
  ]
}`

func widget(r *pricing.Subject, usagetype string) pricing.Query {
	return pricing.Query{Vendor: "test", Service: "Widgets", Region: r.Region(), Attributes: map[string]string{"usagetype": usagetype}}
}

var widgets = pricing.Table{
	"test_widget": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "hours"), Quantity: pricing.MonthlyHours})
		r.Add(pricing.Component{Name: "Requests", Unit: "1M requests", Price: widget(r, "requests"), Allowance: pricing.AllowanceResource, UsageBased: true,
			Quantity: r.Usage("monthly_requests", pricing.Band{Light: 1, Moderate: 3, Heavy: 30})})
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Price: widget(r, "storage"), UsageBased: true,
			Quantity: r.Usage("storage_gb", pricing.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
	"test_gadget": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("size").Mul(pricing.MonthlyHours)})
	},
	"test_reader": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Reads", Unit: "reads", Price: widget(r, "reads"), Allowance: pricing.AllowanceAccount, UsageBased: true,
			Quantity: r.Usage("monthly_reads", pricing.Band{Light: 60, Moderate: 60, Heavy: 600})})
	},
	"test_silent_reader": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Reads", Unit: "reads", Price: widget(r, "reads"), UsageBased: true,
			Quantity: r.Usage("monthly_reads", pricing.Band{Light: 60, Moderate: 60, Heavy: 600})})
	},
	"test_tagged": func(r *pricing.Subject) {
		usagetype := "hours"
		if tags := r.List("tags"); len(tags) > 0 && tags[0] == "premium" {
			usagetype = "premium"
		}
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, usagetype), Quantity: pricing.MonthlyHours})
	},
	"test_blocked": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"),
			Quantity: decimal.NewFromInt(int64(len(r.List("block")))).Mul(r.Number("block.0.size")).Mul(pricing.MonthlyHours)})
	},
	"test_nested": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Price: widget(r, "storage"), UsageBased: true,
			Quantity: r.Usage("standard.storage_gb", pricing.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
	"test_trinket": func(r *pricing.Subject) { r.Free() },
	"test_pair": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("size").Mul(pricing.MonthlyHours)})
		r.Add(pricing.Component{Name: "Count", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("count").Mul(pricing.MonthlyHours)})
	},
	"test_twice": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("size").Mul(pricing.MonthlyHours)})
		r.Add(pricing.Component{Name: "Double", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("size").Mul(pricing.MonthlyHours).Mul(decimal.NewFromInt(2))})
	},
	"test_gated": func(r *pricing.Subject) {
		if r.Bool("premium") {
			r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "premium"), Quantity: pricing.MonthlyHours, Needs: []string{"premium"}})
			return
		}
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "hours"), Quantity: pricing.MonthlyHours, Needs: []string{"premium"}})
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Price: widget(r, "storage"), Quantity: decimal.NewFromInt(1), Needs: []string{"premium"}})
	},
	"test_block_size": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("block.0.size").Mul(pricing.MonthlyHours)})
	},
	"test_block_field": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: r.Number("block.size").Mul(pricing.MonthlyHours)})
	},
	"test_optional": func(r *pricing.Subject) {
		if r.When(func() bool { return r.Bool("extra") }) {
			r.Add(pricing.Component{Name: "Extra", Unit: "GB-month", Price: widget(r, "storage"), Quantity: decimal.NewFromInt(1), Needs: []string{"extra"}})
		}
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "hours"), Quantity: pricing.MonthlyHours})
	},
	"test_conjoined": func(r *pricing.Subject) {
		size := r.NumberOr("size", decimal.NewFromInt(1))
		if r.When(func() bool { return r.Bool("extra") }, func() bool { return r.NumberOr("count", decimal.Zero).IsPositive() }) {
			r.Add(pricing.Component{Name: "Extra", Unit: "GB-month", Price: widget(r, "storage"), Quantity: decimal.NewFromInt(1), Needs: []string{"extra", "count"}})
		}
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "hours"), Quantity: size.Mul(pricing.MonthlyHours)})
	},
	"test_defaulted": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"),
			Quantity: r.NumberOr("size", decimal.NewFromInt(3)).Mul(pricing.MonthlyHours)})
	},
	"test_account_hours": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Base", Unit: "hour", Price: widget(r, "hours"), Allowance: pricing.AllowanceAccount, Quantity: pricing.MonthlyHours})
	},
	"test_attached": func(r *pricing.Subject) {
		host, _ := r.Reference("host")
		r.Add(pricing.Component{Name: "Size", Unit: "hour", Price: widget(r, "hours"), Quantity: host.Number("size").Mul(pricing.MonthlyHours)})
	},
	"test_twin": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Twin", Unit: "hour", Price: widget(r, "twin"), Quantity: pricing.MonthlyHours})
	},
	"test_gizmo": func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Each", Unit: "each", Price: pricing.Query{Vendor: "test", Service: "Gizmos", Attributes: map[string]string{"usagetype": "each"}},
			Quantity: decimal.NewFromInt(3)})
		r.Add(pricing.Component{Name: "Hours", Unit: "hour", Price: widget(r, "hours"), Quantity: pricing.MonthlyHours})
	},
}

func resource(id, scope, typ, region string, props map[string]any, unknown ...string) *costv1.Resource {
	properties, _ := structpb.NewStruct(props)
	return &costv1.Resource{Id: id, Scope: scope, Vendor: "test", Type: typ, Name: id, Region: region, Properties: properties, Unknown: unknown}
}

func set(resources ...*costv1.Resource) *costv1.ResourceSet {
	if len(resources) == 0 {
		resources = []*costv1.Resource{
			resource("w", "p/e/a", "test_widget", "eu-west-1", map[string]any{}),
			resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 2}),
			resource("t", "p/e", "test_trinket", "eu-west-1", map[string]any{}),
			resource("x", "p/e", "test_unknown_thing", "eu-west-1", map[string]any{}),
		}
	}
	return &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{
			{Id: "p", Kind: "project", Name: "shop"},
			{Id: "p/e", Parent: "p", Kind: "environment", Name: "production"},
			{Id: "p/e/a", Parent: "p/e", Kind: "app", Name: "web"},
		},
		Resources: resources,
	}
}

func loaded(t *testing.T) *pricingtest.Card {
	t.Helper()
	c, err := pricingtest.NewCard([]byte(card))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func estimateWith(t *testing.T, catalog pricing.Catalog, table pricing.Table, resources *costv1.ResourceSet, usage *costv1.Usage) *costv1.Estimate {
	t.Helper()
	got, err := pricing.Estimate(context.Background(), catalog, table, resources, usage)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func estimate(t *testing.T, resources *costv1.ResourceSet, usage *costv1.Usage) *costv1.Estimate {
	t.Helper()
	return estimateWith(t, loaded(t), widgets, resources, usage)
}

func byID(est *costv1.Estimate, id string) *costv1.ResourceEstimate {
	for _, r := range est.GetResources() {
		if r.GetResource() == id {
			return r
		}
	}
	return nil
}

func component(r *costv1.ResourceEstimate, name string) *costv1.CostComponent {
	for _, c := range r.GetComponents() {
		if c.GetName() == name {
			return c
		}
	}
	return nil
}

func TestAModerateMonthPricesFixedAndUsageApart(t *testing.T) {
	est := estimate(t, set(), nil)

	if est.GetProfile() != costv1.Profile_PROFILE_MODERATE || est.GetCurrency() != "USD" {
		t.Fatalf("header = %s %s", est.GetProfile(), est.GetCurrency())
	}
	widget := byID(est, "w")
	if widget.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
		t.Fatalf("widget status = %v", widget.GetStatus())
	}
	if widget.GetMonthlyFixed() != "73.00" {
		t.Errorf("widget fixed = %s, want 73.00 (730 h at 0.10)", widget.GetMonthlyFixed())
	}
	if widget.GetMonthlyUsage() != "0.90" {
		t.Errorf("widget usage = %s, want 0.90 (2M billable requests at 0.20 + 10 GB at 0.05)", widget.GetMonthlyUsage())
	}
	if gadget := byID(est, "g"); gadget.GetMonthlyFixed() != "146.00" {
		t.Errorf("gadget fixed = %s, want 146.00 (size 2 for 730 h at 0.10)", gadget.GetMonthlyFixed())
	}
	if est.GetMonthlyFixed() != "219.00" || est.GetMonthlyUsage() != "0.90" {
		t.Errorf("totals = %s + %s, want 219.00 + 0.90", est.GetMonthlyFixed(), est.GetMonthlyUsage())
	}
}

func TestAPricedComponentNamesTheSKUItsSourceAndItsMarginalPrice(t *testing.T) {
	base := component(byID(estimate(t, set(), nil), "w"), "Base")

	if base.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED || base.GetSku() != "HOURS" ||
		base.GetPriceSource() != "https://example.test/hours" || base.GetUnitPrice() != "0.1" || base.GetMonthlyQuantity() != "730" {
		t.Errorf("base = %v", base)
	}
}

func TestAnEstimateNamesEachCatalogVersionItPricedFromOnce(t *testing.T) {
	est := estimate(t, set(
		resource("z", "p/e", "test_gizmo", "eu-west-1", map[string]any{}),
		resource("w", "p/e", "test_widget", "eu-west-1", map[string]any{}),
	), nil)

	var got []string
	for _, c := range est.GetCatalogs() {
		got = append(got, c.GetVendor()+"/"+c.GetService()+"@"+c.GetVersion())
	}
	if strings.Join(got, ",") != "test/Gizmos@2026-09-01,test/Widgets@2026-09-01" {
		t.Errorf("catalogs = %v, want each priced service once, sorted", got)
	}
}

func TestEveryQueryGoesToTheCatalogInOneLookupOnce(t *testing.T) {
	c := loaded(t)
	estimateWith(t, c, widgets, set(
		resource("a", "p/e", "test_pair", "eu-west-1", map[string]any{"size": 1, "count": 2}),
		resource("b", "p/e", "test_widget", "eu-west-1", map[string]any{}),
		resource("u", "p/e", "test_gadget", "us-east-1", map[string]any{}, "size"),
	), nil)

	lookups := c.Lookups()
	if len(lookups) != 1 {
		t.Fatalf("lookups = %d, want 1", len(lookups))
	}
	keys := map[string]bool{}
	for _, query := range lookups[0] {
		if keys[query.Key()] {
			t.Errorf("query %+v sent twice", query)
		}
		keys[query.Key()] = true
		if query.Region == "us-east-1" {
			t.Errorf("query %+v sent for a component over an unknown input", query)
		}
	}
	if len(keys) != 3 {
		t.Errorf("queries = %v, want hours, requests and storage once each", lookups[0])
	}
}

func TestASetWithNothingToPriceNeverCallsTheCatalog(t *testing.T) {
	c := loaded(t)
	estimateWith(t, c, widgets, set(resource("t", "p/e", "test_trinket", "eu-west-1", map[string]any{})), nil)

	if got := c.Lookups(); len(got) != 0 {
		t.Errorf("lookups = %v, want none", got)
	}
}

type failing struct{}

func (failing) Lookup(context.Context, []pricing.Query) ([]pricing.Answer, error) {
	return nil, errors.New("console unreachable")
}

type short struct{}

func (short) Lookup(context.Context, []pricing.Query) ([]pricing.Answer, error) {
	return []pricing.Answer{}, nil
}

func TestAFailedOrShortLookupFailsTheEstimate(t *testing.T) {
	for name, catalog := range map[string]pricing.Catalog{"failed": failing{}, "short": short{}} {
		t.Run(name, func(t *testing.T) {
			est, err := pricing.Estimate(context.Background(), catalog, widgets, set(), nil)
			if err == nil || est != nil {
				t.Fatalf("Estimate() = %v, %v; want an error and no estimate", est, err)
			}
		})
	}
	if _, err := pricing.Estimate(context.Background(), failing{}, widgets, set(), nil); !strings.Contains(err.Error(), "console unreachable") {
		t.Errorf("error = %v, want the lookup's cause wrapped", err)
	}
}

func TestScopesRollUpTheirDescendants(t *testing.T) {
	est := estimate(t, set(), nil)

	want := map[string][2]string{"p": {"219.00", "0.90"}, "p/e": {"219.00", "0.90"}, "p/e/a": {"73.00", "0.90"}}
	for _, scope := range est.GetScopes() {
		if got := [2]string{scope.GetMonthlyFixed(), scope.GetMonthlyUsage()}; got != want[scope.GetScope()] {
			t.Errorf("scope %s = %v, want %v", scope.GetScope(), got, want[scope.GetScope()])
		}
	}
	if len(est.GetScopes()) != 3 {
		t.Errorf("scopes = %d, want 3", len(est.GetScopes()))
	}
}

func TestCoverageCountsWhatWasNotPriced(t *testing.T) {
	est := estimate(t, set(), nil)

	cov := est.GetCoverage()
	if cov.GetSupported() != 2 || cov.GetFree() != 1 || cov.GetUnsupported() != 1 || cov.GetNoPrice() != 0 {
		t.Errorf("coverage = %v", cov)
	}
	if cov.GetUnsupportedTypes()["test_unknown_thing"] != 1 {
		t.Errorf("unsupported types = %v", cov.GetUnsupportedTypes())
	}
	if strings.Join(cov.GetSupportedTypes(), ",") != "test_gadget,test_trinket,test_widget" {
		t.Errorf("supported types = %v", cov.GetSupportedTypes())
	}
	if byID(est, "x").GetStatus() != costv1.ResourceEstimate_STATUS_UNSUPPORTED || byID(est, "t").GetStatus() != costv1.ResourceEstimate_STATUS_FREE {
		t.Errorf("statuses: x=%v t=%v", byID(est, "x").GetStatus(), byID(est, "t").GetStatus())
	}
}

func TestAProfileAndAUsageFileChangeOnlyUsage(t *testing.T) {
	override, _ := structpb.NewStruct(map[string]any{"storage_gb": 200})
	est := estimate(t, set(), &costv1.Usage{
		Profile:   costv1.Profile_PROFILE_HEAVY,
		Resources: map[string]*structpb.Struct{"w": override},
	})

	widget := byID(est, "w")
	if widget.GetMonthlyFixed() != "73.00" {
		t.Errorf("fixed moved to %s", widget.GetMonthlyFixed())
	}
	if widget.GetMonthlyUsage() != "15.80" {
		t.Errorf("usage = %s, want 15.80 (29M billable requests at 0.20 + 200 GB at 0.05)", widget.GetMonthlyUsage())
	}
	if storage := component(widget, "Storage"); storage.GetAssumption() != "storage_gb from usage file" || storage.GetMonthlyQuantity() != "200" {
		t.Errorf("storage component = %v", storage)
	}
}

func TestAMissingPriceIsSaidNotGuessed(t *testing.T) {
	est := estimate(t, set(resource("w", "p/e/a", "test_widget", "us-east-1", map[string]any{})), nil)

	widget := byID(est, "w")
	for _, c := range widget.GetComponents() {
		if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING || c.GetPriceDetail() == "" || c.GetMonthlyCost() != "" || c.GetMonthlyQuantity() == "" {
			t.Errorf("%s in a region the catalog lacks = %v, want missing with its reason, its quantity and no cost", c.GetName(), c)
		}
	}
	if widget.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE || est.GetCoverage().GetNoPrice() != 1 {
		t.Errorf("widget = %v, coverage %v", widget.GetStatus(), est.GetCoverage())
	}
}

func TestAnAmbiguousPriceNamesItsCandidates(t *testing.T) {
	twin := byID(estimate(t, set(resource("w", "p/e", "test_twin", "eu-west-1", map[string]any{})), nil), "w").GetComponents()[0]

	if twin.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_AMBIGUOUS || twin.GetPriceDetail() != "matches 2 skus: TWIN-A, TWIN-B" || twin.GetMonthlyCost() != "" {
		t.Errorf("twin = %v", twin)
	}
}

type offline struct{}

func (offline) Lookup(_ context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	answers := make([]pricing.Answer, len(queries))
	for i := range answers {
		answers[i] = pricing.Answer{Kind: pricing.AnswerUnavailable, Reason: "not cached"}
	}
	return answers, nil
}

func TestAnUnavailablePriceIsSaidAndCountedUnpriced(t *testing.T) {
	est := estimateWith(t, offline{}, widgets, set(resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 1})), nil)

	gadget := byID(est, "g")
	size := gadget.GetComponents()[0]
	if size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNAVAILABLE || size.GetPriceDetail() != "not cached" || size.GetMonthlyCost() != "" {
		t.Errorf("size = %v", size)
	}
	if gadget.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE || est.GetCoverage().GetNoPrice() != 1 {
		t.Errorf("gadget = %v, coverage %v", gadget.GetStatus(), est.GetCoverage())
	}
}

func TestAnUnknownPropertyNeverPricesToZero(t *testing.T) {
	est := estimate(t, set(resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{}, "size")), nil)

	gadget := byID(est, "g")
	size := gadget.GetComponents()[0]
	if size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT || size.GetMonthlyCost() != "" ||
		size.GetMonthlyQuantity() != "" || strings.Join(size.GetDependsOnUnknown(), ",") != "size" {
		t.Errorf("component over an unknown = %v", size)
	}
	if gadget.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE || gadget.GetMonthlyFixed() != "" {
		t.Errorf("gadget = %v fixed %q", gadget.GetStatus(), gadget.GetMonthlyFixed())
	}
	if est.GetCoverage().GetNoPrice() != 1 {
		t.Errorf("coverage = %v", est.GetCoverage())
	}
}

func TestAnUnknownRegionLeavesOnlyRegionalComponentsUnpriced(t *testing.T) {
	est := estimate(t, set(resource("z", "p/e", "test_gizmo", "", map[string]any{}, "region")), nil)

	gizmo := byID(est, "z")
	if each := component(gizmo, "Each"); each.GetMonthlyCost() != "6.00" {
		t.Errorf("each = %v, want 6.00 from a global price that never reads the region", each)
	}
	hours := component(gizmo, "Hours")
	if hours.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT || strings.Join(hours.GetDependsOnUnknown(), ",") != "region" {
		t.Errorf("hours = %v, want it unpriced over the unknown region", hours)
	}
}

func TestAnAccountAllowanceIsSpentOnceAcrossTheResourcesSharingIt(t *testing.T) {
	est := estimate(t, set(
		resource("r1", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r2", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r3", "p/e", "test_reader", "eu-west-1", map[string]any{}),
	), nil)

	if byID(est, "r1").GetMonthlyUsage() != "0.00" || byID(est, "r2").GetMonthlyUsage() != "0.20" || byID(est, "r3").GetMonthlyUsage() != "0.60" {
		t.Errorf("readers = %s, %s, %s; want the 100 free reads spent by the first two, then 0.01 a read",
			byID(est, "r1").GetMonthlyUsage(), byID(est, "r2").GetMonthlyUsage(), byID(est, "r3").GetMonthlyUsage())
	}
	if est.GetMonthlyUsage() != "0.80" {
		t.Errorf("total = %s, want 0.80 (180 reads of 80 past one shared allowance)", est.GetMonthlyUsage())
	}
}

func TestAnAccountAllowanceSaysOnceThatItIsAssumedUnspent(t *testing.T) {
	est := estimate(t, set(
		resource("r1", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r2", "p/e", "test_reader", "eu-west-1", map[string]any{}),
	), nil)

	if strings.Join(est.GetNotes(), "|") != pricing.AllowanceAssumed {
		t.Errorf("notes = %v, want the unspent-allowance assumption once", est.GetNotes())
	}
	if notes := estimate(t, set(), nil).GetNotes(); slices.Contains(notes, pricing.AllowanceAssumed) {
		t.Errorf("notes = %v, want no account assumption where only a per-resource allowance applied", notes)
	}
}

func TestAFreeFirstTierNoRuleScopesIsSpentOncePerAccount(t *testing.T) {
	est := estimate(t, set(
		resource("r1", "p/e", "test_silent_reader", "eu-west-1", map[string]any{}),
		resource("r2", "p/e", "test_silent_reader", "eu-west-1", map[string]any{}),
	), nil)

	if byID(est, "r1").GetMonthlyUsage() != "0.00" || byID(est, "r2").GetMonthlyUsage() != "0.20" {
		t.Errorf("readers = %s, %s; want the free tier spent once", byID(est, "r1").GetMonthlyUsage(), byID(est, "r2").GetMonthlyUsage())
	}
	if !slices.Contains(est.GetNotes(), pricing.AllowanceAssumed) {
		t.Errorf("notes = %v, want the unspent-allowance assumption", est.GetNotes())
	}
}

func TestAnUnknownTaintsOnlyTheComponentsThatReadIt(t *testing.T) {
	est := estimate(t, set(
		resource("pair", "p/e", "test_pair", "eu-west-1", map[string]any{"count": 2}, "size"),
		resource("twice", "p/e", "test_twice", "eu-west-1", map[string]any{}, "size"),
		resource("gated", "p/e", "test_gated", "eu-west-1", map[string]any{}, "premium"),
	), nil)

	pair := byID(est, "pair")
	size, count := pair.GetComponents()[0], pair.GetComponents()[1]
	if size.GetMonthlyCost() != "" || strings.Join(size.GetDependsOnUnknown(), ",") != "size" {
		t.Errorf("size = %v, want it unpriced over the unknown", size)
	}
	if count.GetMonthlyCost() != "146.00" || len(count.GetDependsOnUnknown()) != 0 {
		t.Errorf("count = %v, want 146.00 untouched by an unknown it never read", count)
	}
	if pair.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED || pair.GetMonthlyFixed() != "146.00" {
		t.Errorf("pair = %v %s", pair.GetStatus(), pair.GetMonthlyFixed())
	}
	for _, c := range byID(est, "twice").GetComponents() {
		if c.GetMonthlyCost() != "" || strings.Join(c.GetDependsOnUnknown(), ",") != "size" {
			t.Errorf("%s = %v, want every component that reads the unknown left unpriced and naming it once", c.GetName(), c)
		}
	}
	gated := byID(est, "gated")
	if len(gated.GetComponents()) != 2 {
		t.Fatalf("gated = %v, want the two components of the branch an unknown gate falls into", gated)
	}
	for _, c := range gated.GetComponents() {
		if c.GetMonthlyCost() != "" || strings.Join(c.GetDependsOnUnknown(), ",") != "premium" {
			t.Errorf("%s = %v, want every component behind the unknown gate left unpriced and naming it once", c.GetName(), c)
		}
	}
}

func TestAnUnknownListIsNeverReadAsEmpty(t *testing.T) {
	est := estimate(t, set(
		resource("known", "p/e", "test_tagged", "eu-west-1", map[string]any{"tags": []any{"premium"}}),
		resource("blurred", "p/e", "test_tagged", "eu-west-1", map[string]any{}, "tags"),
	), nil)

	if byID(est, "known").GetMonthlyFixed() != "730.00" {
		t.Errorf("premium = %s, want 730.00 at the premium rate", byID(est, "known").GetMonthlyFixed())
	}
	blurred := byID(est, "blurred").GetComponents()[0]
	if strings.Join(blurred.GetDependsOnUnknown(), ",") != "tags" || blurred.GetMonthlyCost() != "" {
		t.Errorf("component over an unknown list = %v, want it left unpriced and naming tags", blurred)
	}
}

func TestAOneElementBlockReadsTheSameWrappedInAListOrNot(t *testing.T) {
	est := estimate(t, set(
		resource("listed", "p/e", "test_blocked", "eu-west-1", map[string]any{"block": []any{map[string]any{"size": 2}}}),
		resource("flat", "p/e", "test_blocked", "eu-west-1", map[string]any{"block": map[string]any{"size": 2}}),
		resource("listed-unindexed", "p/e", "test_block_field", "eu-west-1", map[string]any{"block": []any{map[string]any{"size": 2}}}),
		resource("flat-unindexed", "p/e", "test_block_field", "eu-west-1", map[string]any{"block": map[string]any{"size": 2}}),
	), nil)

	for _, id := range []string{"listed", "flat", "listed-unindexed", "flat-unindexed"} {
		if got := byID(est, id).GetMonthlyFixed(); got != "146.00" {
			t.Errorf("%s = %s, want 146.00 (one block of size 2 for 730 h at 0.10)", id, got)
		}
	}
}

func TestCombinedRulesPriceEachVendorsTypesAndAddTheirNotesOnce(t *testing.T) {
	table := pricing.Combine(
		pricing.Rules{Table: widgets, Notes: []string{"widgets are test-priced"}},
		pricing.Rules{Table: pricing.Table{"other_thing": func(r *pricing.Subject) { r.Free() }}, Notes: []string{"other things are free"}},
	)
	est := estimateWith(t, loaded(t), table, set(
		resource("g1", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 1}),
		resource("g2", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 1}),
	), nil)

	if strings.Join(est.GetNotes(), "|") != "widgets are test-priced" {
		t.Errorf("notes = %v, want the notes of the vendor whose resources are in the set, once", est.GetNotes())
	}
	if byID(est, "g1").GetMonthlyFixed() != "73.00" {
		t.Errorf("gadget = %s", byID(est, "g1").GetMonthlyFixed())
	}
}

func TestAUsageFileReachesANestedKey(t *testing.T) {
	override, _ := structpb.NewStruct(map[string]any{"standard": map[string]any{"storage_gb": 40}})
	est := estimate(t, set(resource("n", "p/e", "test_nested", "eu-west-1", map[string]any{})),
		&costv1.Usage{Resources: map[string]*structpb.Struct{"n": override}})

	storage := byID(est, "n").GetComponents()[0]
	if storage.GetMonthlyQuantity() != "40" || storage.GetAssumption() != "standard.storage_gb from usage file" {
		t.Errorf("nested override = %v", storage)
	}
}

func TestAUsageFileValueThatIsNotAFiniteNonNegativeNumberIsRefused(t *testing.T) {
	for name, value := range map[string]any{
		"quoted":   "200",
		"null":     nil,
		"negative": -1,
		"nan":      math.NaN(),
		"infinite": math.Inf(1),
	} {
		t.Run(name, func(t *testing.T) {
			override, err := structpb.NewStruct(map[string]any{"storage_gb": value})
			if err != nil {
				t.Fatal(err)
			}
			_, err = pricing.Estimate(context.Background(), loaded(t), widgets, set(), &costv1.Usage{
				Resources: map[string]*structpb.Struct{"w": override},
			})
			var usage *pricing.UsageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), "w") || !strings.Contains(err.Error(), "storage_gb") {
				t.Fatalf("Estimate() error = %v, want a usage refusal naming resource w and key storage_gb", err)
			}
		})
	}
}

func TestAUsageFileKeyNoFormulaReadsIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		resource string
		override map[string]any
		key      string
	}{
		"misspelled":        {"w", map[string]any{"storage_gbs": 200}, "storage_gbs"},
		"misspelled nested": {"n", map[string]any{"standard": map[string]any{"storage_gbs": 40}}, "standard.storage_gbs"},
		"flattened nested":  {"n", map[string]any{"standard": 40}, "standard"},
		"read by no branch": {"g", map[string]any{"monthly_requests": 1}, "monthly_requests"},
		"unsupported":       {"x", map[string]any{"monthly_requests": 1}, "x"},
		"no such resource":  {"nope", map[string]any{"monthly_requests": 1}, "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			override, err := structpb.NewStruct(tc.override)
			if err != nil {
				t.Fatal(err)
			}
			s := set()
			s.Resources = append(s.Resources, resource("n", "p/e", "test_nested", "eu-west-1", map[string]any{}))
			_, err = pricing.Estimate(context.Background(), loaded(t), widgets, s, &costv1.Usage{
				Resources: map[string]*structpb.Struct{tc.resource: override},
			})
			var usage *pricing.UsageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), tc.resource) || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Estimate() error = %v, want a usage refusal naming resource %s and key %s", err, tc.resource, tc.key)
			}
		})
	}
}

func TestATreeReportsAPropertyItCannotRepresent(t *testing.T) {
	tree := &pricing.Tree{}
	scope := tree.Scope("", pricing.ScopeProject, "shop")
	tree.Add(scope, "test", "test_widget", "w", "eu-west-1", map[string]any{"bad": make(chan int)})
	if _, err := tree.Set("ocel"); err == nil || !strings.Contains(err.Error(), "test_widget:w") {
		t.Fatalf("Set() error = %v, want one naming the resource", err)
	}
}

func TestAnAbsentOrUnparseableInputIsNamedRatherThanUnknownWhileAnExplicitZeroIsZero(t *testing.T) {
	est := estimate(t, set(
		resource("absent", "p/e", "test_gadget", "eu-west-1", map[string]any{}),
		resource("garbled", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": "lots"}),
		resource("zero", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 0}),
	), nil)

	for id, want := range map[string]string{"absent": "size is not set", "garbled": `size is "lots", which the rule cannot read`} {
		size := byID(est, id).GetComponents()[0]
		if size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING || size.GetPriceDetail() != want || len(size.GetDependsOnUnknown()) != 0 {
			t.Errorf("%s size = %v, want it unpriced because %s, not unknown and not priced at 0", id, size, want)
		}
	}
	if zero := byID(est, "zero").GetComponents()[0]; zero.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED || zero.GetMonthlyCost() != "0.00" {
		t.Errorf("zero size = %v, want it priced at 0.00", zero)
	}
}

func TestADefaultedInputPricesItsDefaultOnlyWhenAbsent(t *testing.T) {
	est := estimate(t, set(
		resource("absent", "p/e", "test_defaulted", "eu-west-1", map[string]any{}),
		resource("garbled", "p/e", "test_defaulted", "eu-west-1", map[string]any{"size": "lots"}),
		resource("set", "p/e", "test_defaulted", "eu-west-1", map[string]any{"size": 1}),
	), nil)

	if got := byID(est, "absent").GetMonthlyFixed(); got != "219.00" {
		t.Errorf("absent = %s, want 219.00 at the default size 3", got)
	}
	if garbled := byID(est, "garbled").GetComponents()[0]; garbled.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING {
		t.Errorf("garbled = %v, want an unparseable value unpriced rather than defaulted", garbled)
	}
	if got := byID(est, "set").GetMonthlyFixed(); got != "73.00" {
		t.Errorf("set = %s, want 73.00", got)
	}
}

func TestAPathIsUnknownWhenItOrAnAncestorIsWhicheverWayItsBlockIsWritten(t *testing.T) {
	for _, unknown := range []string{"block", "block.0", "block.size", "block.0.size"} {
		t.Run(unknown, func(t *testing.T) {
			est := estimate(t, set(resource("b", "p/e", "test_block_size", "eu-west-1", map[string]any{"block": map[string]any{"size": 2}}, unknown)), nil)

			if size := byID(est, "b").GetComponents()[0]; size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
				t.Errorf("size with %s unknown = %v, want it unpriced", unknown, size)
			}
		})
	}
	est := estimate(t, set(resource("b", "p/e", "test_block_size", "eu-west-1", map[string]any{"block": map[string]any{"size": 2}}, "blocks", "block.1.size")), nil)
	if got := byID(est, "b").GetMonthlyFixed(); got != "146.00" {
		t.Errorf("size beside unrelated unknowns = %s, want 146.00", got)
	}
}

func TestAnUnknownGateThatAddsNothingLeavesTheNextComponentPriced(t *testing.T) {
	est := estimate(t, set(
		resource("off", "p/e", "test_optional", "eu-west-1", map[string]any{"extra": false}),
		resource("blurred", "p/e", "test_optional", "eu-west-1", map[string]any{}, "extra"),
	), nil)

	if names := componentNames(byID(est, "off")); names != "Base" {
		t.Errorf("off = %s, want only Base", names)
	}
	blurred := byID(est, "blurred")
	if extra := component(blurred, "Extra"); extra.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("extra behind an unknown gate = %v, want it unpriced", extra)
	}
	if base := component(blurred, "Base"); base.GetMonthlyCost() != "73.00" || len(base.GetDependsOnUnknown()) != 0 {
		t.Errorf("base = %v, want 73.00 untouched by the gate", base)
	}
}

func TestAGateStaysClosedWhenAnyConditionIsKnownFalseWhateverTheOthersRead(t *testing.T) {
	est := estimate(t, set(
		resource("none", "p/e", "test_conjoined", "eu-west-1", map[string]any{"count": 0}, "extra"),
		resource("off", "p/e", "test_conjoined", "eu-west-1", map[string]any{"extra": false}, "count"),
		resource("unset", "p/e", "test_conjoined", "eu-west-1", map[string]any{"count": 0}),
		resource("blurred", "p/e", "test_conjoined", "eu-west-1", map[string]any{"count": 2}, "extra"),
		resource("sized", "p/e", "test_conjoined", "eu-west-1", map[string]any{"extra": false, "count": 2}, "size"),
	), nil)

	for _, id := range []string{"none", "off", "unset", "sized"} {
		if names := componentNames(byID(est, id)); names != "Base" {
			t.Errorf("%s = %s, want only Base behind a gate one condition knows is false", id, names)
		}
	}
	if base := component(byID(est, "none"), "Base"); base.GetMonthlyCost() != "73.00" || len(base.GetDependsOnUnknown()) != 0 {
		t.Errorf("none base = %v, want 73.00 untouched by the closed gate's unknown", base)
	}
	if base := component(byID(est, "sized"), "Base"); strings.Join(base.GetDependsOnUnknown(), ",") != "size" {
		t.Errorf("sized base = %v, want it unknown over the size it read before the gate", base)
	}
	if extra := component(byID(est, "blurred"), "Extra"); extra.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT ||
		strings.Join(extra.GetDependsOnUnknown(), ",") != "extra" {
		t.Errorf("blurred extra = %v, want it unknown over extra", extra)
	}
}

func componentNames(r *costv1.ResourceEstimate) string {
	var names []string
	for _, c := range r.GetComponents() {
		names = append(names, c.GetName())
	}
	return strings.Join(names, ",")
}

func TestAnAccountAllowanceOnAPriceWithNoFreeTierAssumesNothing(t *testing.T) {
	est := estimate(t, set(resource("h", "p/e", "test_account_hours", "eu-west-1", map[string]any{})), nil)

	if notes := est.GetNotes(); slices.Contains(notes, pricing.AllowanceAssumed) {
		t.Errorf("notes = %v, want no allowance assumption where no allowance applied", notes)
	}
}

func TestAScopeThatIsItsOwnAncestorIsRefused(t *testing.T) {
	s := set(resource("g", "a", "test_gadget", "eu-west-1", map[string]any{"size": 1}))
	s.Scopes = []*costv1.Scope{{Id: "a", Parent: "b", Kind: "module", Name: "a"}, {Id: "b", Parent: "a", Kind: "module", Name: "b"}}

	_, err := pricing.Estimate(context.Background(), loaded(t), widgets, s, nil)
	if err == nil || !strings.Contains(err.Error(), "scope a ") {
		t.Fatalf("Estimate() error = %v, want a refusal naming scope a", err)
	}
}

func TestAReferencedResourcesPropertiesPriceTheReferrer(t *testing.T) {
	host := resource("host", "p/e", "test_trinket", "eu-west-1", map[string]any{"size": 2})
	attached := resource("attached", "p/e", "test_attached", "eu-west-1", map[string]any{})
	attached.References = map[string]string{"host": "host"}
	blurredHost := resource("blurred-host", "p/e", "test_trinket", "eu-west-1", map[string]any{}, "size")
	blurred := resource("blurred", "p/e", "test_attached", "eu-west-1", map[string]any{})
	blurred.References = map[string]string{"host": "blurred-host"}
	dangling := resource("dangling", "p/e", "test_attached", "eu-west-1", map[string]any{})
	dangling.References = map[string]string{"host": "nowhere"}
	elsewhere := resource("elsewhere", "p/e", "test_attached", "eu-west-1", map[string]any{"host": "arn:host"})
	unreferenced := resource("unreferenced", "p/e", "test_attached", "eu-west-1", map[string]any{})
	pending := resource("pending", "p/e", "test_attached", "eu-west-1", map[string]any{}, "host")

	est := estimate(t, set(host, attached, blurredHost, blurred, dangling, elsewhere, unreferenced, pending), nil)

	if got := byID(est, "attached").GetMonthlyFixed(); got != "146.00" {
		t.Errorf("attached = %s, want 146.00 from its host's size 2", got)
	}
	for id, want := range map[string]string{"blurred": "host.size", "pending": "host"} {
		size := byID(est, id).GetComponents()[0]
		if size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT || strings.Join(size.GetDependsOnUnknown(), ",") != want {
			t.Errorf("%s = %v, want it unknown over %s", id, size, want)
		}
	}
	for id, want := range map[string]string{
		"dangling":     "host names no resource in this scan",
		"elsewhere":    "host names no resource in this scan",
		"unreferenced": "host is not set",
	} {
		size := byID(est, id).GetComponents()[0]
		if size.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING || size.GetPriceDetail() != want {
			t.Errorf("%s = %v, want it unpriced because %s", id, size, want)
		}
	}
}

func TestEstimatingSeveralInputsAsksTheCatalogOnceAndPricesEachApart(t *testing.T) {
	c := loaded(t)
	inputs := []pricing.Input{
		{Set: set(resource("r", "p/e", "test_reader", "eu-west-1", map[string]any{}))},
		{Set: set(resource("r", "p/e", "test_reader", "eu-west-1", map[string]any{}), resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 1}))},
	}

	got, err := pricing.EstimateAll(context.Background(), c, widgets, inputs)
	if err != nil {
		t.Fatal(err)
	}

	if lookups := c.Lookups(); len(lookups) != 1 || len(lookups[0]) != 2 {
		t.Fatalf("lookups = %v, want one of reads and hours", lookups)
	}
	if len(got) != 2 || byID(got[0], "r").GetMonthlyUsage() != "0.00" || byID(got[1], "r").GetMonthlyUsage() != "0.00" || got[1].GetMonthlyFixed() != "73.00" {
		t.Errorf("estimates = %v, want each input's allowance spent apart", got)
	}
}

type crowded struct{}

func (crowded) Lookup(_ context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	candidates := make([]string, 12)
	for i := range candidates {
		candidates[i] = string(rune('A' + i))
	}
	answers := make([]pricing.Answer, len(queries))
	for i := range answers {
		answers[i] = pricing.Answer{Kind: pricing.AnswerAmbiguous, Candidates: candidates, Matches: 40}
	}
	return answers, nil
}

func TestAnAmbiguityNamesAtMostTenCandidatesAndCountsEveryMatch(t *testing.T) {
	twin := byID(estimateWith(t, crowded{}, widgets, set(resource("w", "p/e", "test_twin", "eu-west-1", map[string]any{})), nil), "w").GetComponents()[0]

	if twin.GetPriceDetail() != "matches 40 skus: A, B, C, D, E, F, G, H, I, J" {
		t.Errorf("detail = %q", twin.GetPriceDetail())
	}
}

func TestAComponentIsUnpricedUnlessItsPriceStatusIsPriced(t *testing.T) {
	for status := range costv1.CostComponent_PriceStatus_name {
		c := &costv1.CostComponent{PriceStatus: costv1.CostComponent_PriceStatus(status)}
		if want := c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED; pricing.Unpriced(c) != want {
			t.Errorf("Unpriced(%v) = %v, want %v", c.GetPriceStatus(), !want, want)
		}
	}
}
