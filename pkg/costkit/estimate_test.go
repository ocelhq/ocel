package costkit_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const card = `{
  "version": "2026-09-01",
  "currency": "USD",
  "rates": [
    {"id": "widget/hours", "region": "", "unit": "hour", "tiers": [{"price": "0.10"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"},
    {"id": "widget/requests", "region": "", "unit": "1M requests",
     "tiers": [{"price": "0"}, {"start": "1", "price": "0.20"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"},
    {"id": "widget/storage", "region": "eu-west-1", "unit": "GB-month", "tiers": [{"price": "0.05"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"}
  ]
}`

var widgets = costkit.Table{
	"test_widget": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Standing", Unit: "hour", Rate: "widget/hours", Quantity: costkit.MonthlyHours})
		r.Add(costkit.Component{Name: "Requests", Unit: "1M requests", Rate: "widget/requests", UsageBased: true,
			Quantity: r.Usage("monthly_requests", costkit.Band{Light: 1, Moderate: 3, Heavy: 30})})
		r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "widget/storage", UsageBased: true,
			Quantity: r.Usage("storage_gb", costkit.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
	"test_gadget": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Size", Unit: "hour", Rate: "widget/hours", Quantity: r.Number("size").Mul(costkit.MonthlyHours)})
	},
	"test_trinket": func(r *costkit.Subject) { r.Free() },
}

func resource(id, scope, typ, region string, props map[string]any, unknown ...string) *costv1.Resource {
	properties, _ := structpb.NewStruct(props)
	return &costv1.Resource{Id: id, Scope: scope, Vendor: "test", Type: typ, Name: id, Region: region, Properties: properties, Unknown: unknown}
}

func set() *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{
			{Id: "p", Kind: "project", Name: "shop"},
			{Id: "p/e", Parent: "p", Kind: "environment", Name: "production"},
			{Id: "p/e/a", Parent: "p/e", Kind: "app", Name: "web"},
		},
		Resources: []*costv1.Resource{
			resource("w", "p/e/a", "test_widget", "eu-west-1", map[string]any{}),
			resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{"size": 2}),
			resource("t", "p/e", "test_trinket", "eu-west-1", map[string]any{}),
			resource("x", "p/e", "test_unknown_thing", "eu-west-1", map[string]any{}),
		},
	}
}

func estimate(t *testing.T, req *costv1.PriceRequest) *costv1.Estimate {
	t.Helper()
	loaded, err := costkit.Load([]byte(card))
	if err != nil {
		t.Fatal(err)
	}
	got, err := costkit.Estimate(loaded, widgets, req)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func byID(est *costv1.Estimate, id string) *costv1.ResourceEstimate {
	for _, r := range est.GetResources() {
		if r.GetResource() == id {
			return r
		}
	}
	return nil
}

func TestAModerateMonthPricesFixedAndUsageApart(t *testing.T) {
	est := estimate(t, &costv1.PriceRequest{Resources: set()})

	if est.GetProfile() != "moderate" || est.GetCurrency() != "USD" || est.GetRatesVersion() != "2026-09-01" {
		t.Fatalf("header = %s %s %s", est.GetProfile(), est.GetCurrency(), est.GetRatesVersion())
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
	gadget := byID(est, "g")
	if gadget.GetMonthlyFixed() != "146.00" {
		t.Errorf("gadget fixed = %s, want 146.00 (size 2 for 730 h at 0.10)", gadget.GetMonthlyFixed())
	}
	if est.GetMonthlyFixed() != "219.00" || est.GetMonthlyUsage() != "0.90" {
		t.Errorf("totals = %s + %s, want 219.00 + 0.90", est.GetMonthlyFixed(), est.GetMonthlyUsage())
	}
}

func TestScopesRollUpTheirDescendants(t *testing.T) {
	est := estimate(t, &costv1.PriceRequest{Resources: set()})

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
	est := estimate(t, &costv1.PriceRequest{Resources: set()})

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
	est := estimate(t, &costv1.PriceRequest{Resources: set(), Usage: &costv1.Usage{
		Profile:   "heavy",
		Resources: map[string]*structpb.Struct{"w": override},
	}})

	widget := byID(est, "w")
	if widget.GetMonthlyFixed() != "73.00" {
		t.Errorf("fixed moved to %s", widget.GetMonthlyFixed())
	}
	if widget.GetMonthlyUsage() != "15.80" {
		t.Errorf("usage = %s, want 15.80 (29M billable requests at 0.20 + 200 GB at 0.05)", widget.GetMonthlyUsage())
	}
	var storage *costv1.CostComponent
	for _, c := range widget.GetComponents() {
		if c.GetName() == "Storage" {
			storage = c
		}
	}
	if storage.GetAssumption() != "storage_gb from usage file" || storage.GetMonthlyQuantity() != "200" {
		t.Errorf("storage component = %v", storage)
	}
}

func TestAMissingRateIsSaidNotGuessed(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{resource("w", "p/e/a", "test_widget", "us-east-1", map[string]any{})}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	widget := byID(est, "w")
	var storage *costv1.CostComponent
	for _, c := range widget.GetComponents() {
		if c.GetName() == "Storage" {
			storage = c
		}
	}
	if !storage.GetPriceNotFound() || storage.GetMonthlyCost() != "" {
		t.Errorf("storage in a region the card lacks = %v, want price_not_found and no cost", storage)
	}
	if widget.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED || widget.GetMonthlyUsage() != "0.40" {
		t.Errorf("widget = %v %s", widget.GetStatus(), widget.GetMonthlyUsage())
	}
	if est.GetCoverage().GetNoPrice() != 0 {
		t.Errorf("a partly priced resource is not a no-price one: %v", est.GetCoverage())
	}
}

func TestAnUnknownPropertyNeverPricesToZero(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{resource("g", "p/e", "test_gadget", "eu-west-1", map[string]any{}, "size")}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	gadget := byID(est, "g")
	size := gadget.GetComponents()[0]
	if size.GetMonthlyCost() != "" || size.GetMonthlyQuantity() != "" || strings.Join(size.GetDependsOnUnknown(), ",") != "size" {
		t.Errorf("component over an unknown = %v", size)
	}
	if gadget.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE || gadget.GetMonthlyFixed() != "" {
		t.Errorf("gadget = %v fixed %q", gadget.GetStatus(), gadget.GetMonthlyFixed())
	}
	if est.GetCoverage().GetNoPrice() != 1 {
		t.Errorf("coverage = %v", est.GetCoverage())
	}
}

func TestACardRefusesARateWithoutProvenance(t *testing.T) {
	_, err := costkit.Load([]byte(`{"version":"v","currency":"USD","rates":[{"id":"a","unit":"h","tiers":[{"price":"1"}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "a") {
		t.Fatalf("Load() error = %v, want one naming rate a", err)
	}
}
