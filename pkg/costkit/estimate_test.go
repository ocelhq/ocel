package costkit_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
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
    {"id": "widget/requests", "region": "", "unit": "1M requests", "allowance": "resource",
     "tiers": [{"price": "0"}, {"start": "1", "price": "0.20"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"},
    {"id": "widget/storage", "region": "eu-west-1", "unit": "GB-month", "tiers": [{"price": "0.05"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"},
    {"id": "widget/reads", "region": "", "unit": "reads", "allowance": "account",
     "tiers": [{"price": "0"}, {"start": "100", "price": "0.01"}],
     "source": "https://example.test/pricing", "verified": "2026-09-01"},
    {"id": "gadget/hours", "region": "", "unit": "hour", "tiers": [{"price": "1"}],
     "note": "gadgets outside eu-west-1 are priced at the eu-west-1 rate",
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
	"test_reader": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Reads", Unit: "reads", Rate: "widget/reads", UsageBased: true,
			Quantity: r.Usage("monthly_reads", costkit.Band{Light: 60, Moderate: 60, Heavy: 600})})
	},
	"test_tagged": func(r *costkit.Subject) {
		rate := "widget/hours"
		if tags := r.List("tags"); len(tags) > 0 && tags[0] == "premium" {
			rate = "gadget/hours"
		}
		r.Add(costkit.Component{Name: "Standing", Unit: "hour", Rate: rate, Quantity: costkit.MonthlyHours})
	},
	"test_nested": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "widget/storage", UsageBased: true,
			Quantity: r.Usage("standard.storage_gb", costkit.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
	"test_trinket": func(r *costkit.Subject) { r.Free() },
	"test_pair": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Size", Unit: "hour", Rate: "widget/hours", Quantity: r.Number("size").Mul(costkit.MonthlyHours)})
		r.Add(costkit.Component{Name: "Count", Unit: "hour", Rate: "widget/hours", Quantity: r.Number("count").Mul(costkit.MonthlyHours)})
	},
	"test_twice": func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Size", Unit: "hour", Rate: "widget/hours", Quantity: r.Number("size").Mul(costkit.MonthlyHours)})
		r.Add(costkit.Component{Name: "Double", Unit: "hour", Rate: "widget/hours", Quantity: r.Number("size").Mul(costkit.MonthlyHours).Mul(decimal.NewFromInt(2))})
	},
	"test_gated": func(r *costkit.Subject) {
		if r.Bool("premium") {
			r.Add(costkit.Component{Name: "Standing", Unit: "hour", Rate: "gadget/hours", Quantity: costkit.MonthlyHours, Needs: []string{"premium"}})
			return
		}
		r.Add(costkit.Component{Name: "Standing", Unit: "hour", Rate: "widget/hours", Quantity: costkit.MonthlyHours, Needs: []string{"premium"}})
		r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "widget/storage", Quantity: decimal.NewFromInt(1), Needs: []string{"premium"}})
	},
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

	if est.GetProfile() != costv1.Profile_PROFILE_MODERATE || est.GetCurrency() != "USD" || est.GetRatesVersion() != "2026-09-01" {
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
		Profile:   costv1.Profile_PROFILE_HEAVY,
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

func TestMergedCardsAndTablesPriceEachVendorsOwn(t *testing.T) {
	primary, _ := costkit.Load([]byte(card))
	other, _ := costkit.Load([]byte(`{"version":"v2","currency":"USD","rates":[
		{"id":"gizmo/each","unit":"each","tiers":[{"price":"2"}],"source":"https://example.test","verified":"2026-09-01"},
		{"id":"widget/hours","unit":"hour","tiers":[{"price":"99"}],"source":"https://example.test","verified":"2026-09-01"}]}`))
	table := costkit.Tables(widgets, costkit.Table{
		"test_gizmo": func(r *costkit.Subject) {
			r.Add(costkit.Component{Name: "Each", Unit: "each", Rate: "gizmo/each", Quantity: decimal.NewFromInt(3)})
		},
	})
	s := set()
	s.Resources = append(s.Resources, resource("z", "p/e", "test_gizmo", "eu-west-1", map[string]any{}))
	est, err := costkit.Estimate(costkit.Merge(primary, other), table, &costv1.PriceRequest{Resources: s})
	if err != nil {
		t.Fatal(err)
	}
	if est.GetRatesVersion() != "2026-09-01" {
		t.Errorf("version = %s, want the primary card's", est.GetRatesVersion())
	}
	if byID(est, "z").GetMonthlyFixed() != "6.00" {
		t.Errorf("gizmo = %s, want 6.00", byID(est, "z").GetMonthlyFixed())
	}
	if byID(est, "w").GetMonthlyFixed() != "73.00" {
		t.Errorf("widget = %s, want the primary card's 0.10 rate to win over the other card's 99", byID(est, "w").GetMonthlyFixed())
	}
}

func TestAnAccountAllowanceIsSpentOnceAcrossTheResourcesSharingIt(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{
		resource("r1", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r2", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r3", "p/e", "test_reader", "eu-west-1", map[string]any{}),
	}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	if byID(est, "r1").GetMonthlyUsage() != "0.00" || byID(est, "r2").GetMonthlyUsage() != "0.20" || byID(est, "r3").GetMonthlyUsage() != "0.60" {
		t.Errorf("readers = %s, %s, %s; want the 100 free reads spent by the first two, then 0.01 a read",
			byID(est, "r1").GetMonthlyUsage(), byID(est, "r2").GetMonthlyUsage(), byID(est, "r3").GetMonthlyUsage())
	}
	if est.GetMonthlyUsage() != "0.80" {
		t.Errorf("total = %s, want 0.80 (180 reads of 80 past one shared allowance)", est.GetMonthlyUsage())
	}
}

func TestAnAccountAllowanceSaysOnceThatItIsAssumedUnspent(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{
		resource("r1", "p/e", "test_reader", "eu-west-1", map[string]any{}),
		resource("r2", "p/e", "test_reader", "eu-west-1", map[string]any{}),
	}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	if strings.Join(est.GetNotes(), "|") != costkit.AllowanceAssumed {
		t.Errorf("notes = %v, want the unspent-allowance assumption once", est.GetNotes())
	}

	if notes := estimate(t, &costv1.PriceRequest{Resources: set()}).GetNotes(); slices.Contains(notes, costkit.AllowanceAssumed) {
		t.Errorf("notes = %v, want no account assumption where only a per-resource allowance applied", notes)
	}
}

func TestAnUnknownTaintsOnlyTheComponentsThatReadIt(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{
		resource("pair", "p/e", "test_pair", "eu-west-1", map[string]any{"count": 2}, "size"),
		resource("twice", "p/e", "test_twice", "eu-west-1", map[string]any{}, "size"),
		resource("gated", "p/e", "test_gated", "eu-west-1", map[string]any{}, "premium"),
	}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

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

func TestACardRefusesAFreeFirstTierWithoutAnAllowanceScope(t *testing.T) {
	_, err := costkit.Load([]byte(`{"version":"v","currency":"USD","rates":[{"id":"a","unit":"h","tiers":[{"price":"0"},{"start":"5","price":"1"}],"source":"s","verified":"v"}]}`))
	if err == nil || !strings.Contains(err.Error(), "allowance") {
		t.Fatalf("Load() error = %v, want a refusal asking whose allowance it is", err)
	}
	_, err = costkit.Load([]byte(`{"version":"v","currency":"USD","rates":[{"id":"a","unit":"h","allowance":"account","tiers":[{"price":"1"}],"source":"s","verified":"v"}]}`))
	if err == nil {
		t.Fatal("Load() accepted an allowance on a rate whose first tier is not free")
	}
}

func TestAnUnknownListIsNeverReadAsEmpty(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{
		resource("known", "p/e", "test_tagged", "eu-west-1", map[string]any{"tags": []any{"premium"}}),
		resource("blurred", "p/e", "test_tagged", "eu-west-1", map[string]any{}, "tags"),
	}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	if byID(est, "known").GetMonthlyFixed() != "730.00" {
		t.Errorf("premium = %s, want 730.00 at the premium rate", byID(est, "known").GetMonthlyFixed())
	}
	blurred := byID(est, "blurred").GetComponents()[0]
	if strings.Join(blurred.GetDependsOnUnknown(), ",") != "tags" || blurred.GetMonthlyCost() != "" {
		t.Errorf("component over an unknown list = %v, want it left unpriced and naming tags", blurred)
	}
}

func TestARegionFallbackCarriesItsNoteOnce(t *testing.T) {
	s := set()
	s.Resources = []*costv1.Resource{
		resource("a", "p/e", "test_tagged", "us-east-1", map[string]any{"tags": []any{"premium"}}),
		resource("b", "p/e", "test_tagged", "us-east-1", map[string]any{"tags": []any{"premium"}}),
		resource("c", "p/e", "test_tagged", "", map[string]any{"tags": []any{"premium"}}),
	}
	est := estimate(t, &costv1.PriceRequest{Resources: s})

	if strings.Join(est.GetNotes(), "|") != "gadgets outside eu-west-1 are priced at the eu-west-1 rate" {
		t.Errorf("notes = %v, want the fallback's note once", est.GetNotes())
	}
	if byID(est, "a").GetMonthlyFixed() != "730.00" {
		t.Errorf("fallback = %s, want the region-less rate", byID(est, "a").GetMonthlyFixed())
	}
}

func TestAUsageFileReachesANestedKey(t *testing.T) {
	override, _ := structpb.NewStruct(map[string]any{"standard": map[string]any{"storage_gb": 40}})
	s := set()
	s.Resources = []*costv1.Resource{resource("n", "p/e", "test_nested", "eu-west-1", map[string]any{})}
	est := estimate(t, &costv1.PriceRequest{Resources: s, Usage: &costv1.Usage{Resources: map[string]*structpb.Struct{"n": override}}})

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
			loaded, _ := costkit.Load([]byte(card))
			_, err = costkit.Estimate(loaded, widgets, &costv1.PriceRequest{Resources: set(), Usage: &costv1.Usage{
				Resources: map[string]*structpb.Struct{"w": override},
			}})
			var usage *costkit.UsageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), "w") || !strings.Contains(err.Error(), "storage_gb") {
				t.Fatalf("Estimate() error = %v, want a usage refusal naming resource w and key storage_gb", err)
			}
		})
	}
}

func TestAUsageFileKeyThePricerNeverReadsIsRefused(t *testing.T) {
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
			loaded, _ := costkit.Load([]byte(card))
			_, err = costkit.Estimate(loaded, widgets, &costv1.PriceRequest{Resources: s, Usage: &costv1.Usage{
				Resources: map[string]*structpb.Struct{tc.resource: override},
			}})
			var usage *costkit.UsageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), tc.resource) || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Estimate() error = %v, want a usage refusal naming resource %s and key %s", err, tc.resource, tc.key)
			}
		})
	}
}

func TestATreeReportsAPropertyItCannotCarry(t *testing.T) {
	tree := &costkit.Tree{}
	scope := tree.Scope("", costkit.ScopeProject, "shop")
	tree.Add(scope, "test", "test_widget", "w", "eu-west-1", map[string]any{"bad": make(chan int)})
	if _, err := tree.Set("ocel"); err == nil || !strings.Contains(err.Error(), "test_widget:w") {
		t.Fatalf("Set() error = %v, want one naming the resource", err)
	}
}
