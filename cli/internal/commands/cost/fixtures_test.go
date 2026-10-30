package cost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	"github.com/ocelhq/ocel/pkg/pricing/source"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

type everyQueryPriced struct{}

func (everyQueryPriced) Lookup(_ context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	answers := make([]pricing.Answer, len(queries))
	for i, query := range queries {
		answers[i] = pricing.Answer{Kind: pricing.AnswerPriced, Price: pricing.Price{
			SKU:            "SKU-" + query.Service,
			Unit:           query.Unit,
			Steps:          []pricing.Step{{Start: decimal.Zero, Price: decimal.RequireFromString("0.01")}},
			CatalogVersion: "20261028000000",
			Source:         "https://prices.example.test/" + query.Vendor,
		}}
	}
	return answers, nil
}

func parserFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "pkg", "pricing", "source", "*", "testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []string
	for _, path := range paths {
		if !strings.HasSuffix(path, ".golden.json") {
			fixtures = append(fixtures, path)
		}
	}
	if len(fixtures) == 0 {
		t.Fatal("found no parser fixtures under pkg/pricing/source")
	}
	return fixtures
}

func TestEveryParserFixtureScansAgainstAConsoleWithTheRulesTheCLIShips(t *testing.T) {
	for _, path := range parserFixtures(t) {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path)))+"/"+filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			document, err := source.Read(raw)
			if err != nil {
				t.Fatalf("source.Read() = %v", err)
			}
			prices := servePrices(t, everyQueryPriced{})
			dependencies := standaloneDependencies(t)
			dependencies.Prices.Table = ruleset.Table()
			asJSON(&dependencies)

			got := decodeScan(t, scan(t, dependencies, t.TempDir(), Options{From: path}))

			if n, want := len(got.GetEstimate().GetResources()), len(got.GetResources().GetResources()); n != want || want == 0 {
				t.Errorf("estimated %d resources, want each of the %d the document holds", n, want)
			}
			if got.GetEstimate().GetCoverage().GetSupported() == 0 {
				t.Errorf("coverage = %v, want the shipped rules to price something in %s", got.GetEstimate().GetCoverage(), path)
			}
			if calls := len(prices.Calls()); calls != 1 {
				t.Errorf("calls = %d, want one lookup", calls)
			}

			if document.Prior == nil {
				return
			}
			diff := decodeDiff(t, scan(t, dependencies, t.TempDir(), Options{From: path, Diff: true}))
			if diff.GetDiff().GetProposed() == nil || diff.GetDiff().GetCurrent() == nil {
				t.Errorf("diff = %v, want both sides priced", diff.GetDiff())
			}
			for _, change := range diff.GetDiff().GetResources() {
				if change.GetChange() == costv1.ResourceChange_CHANGE_UNSPECIFIED {
					t.Errorf("change %v names no kind", change)
				}
			}
		})
	}
}

func TestAFixturePlanAgainstTheStateItAppliedMovesNoCost(t *testing.T) {
	testdata := filepath.Join("..", "..", "..", "..", "pkg", "pricing", "source", "terraform", "testdata")
	servePrices(t, everyQueryPriced{})
	dependencies := standaloneDependencies(t)
	dependencies.Prices.Table = ruleset.Table()
	asJSON(&dependencies)

	diff := decodeDiff(t, scan(t, dependencies, t.TempDir(), Options{
		From: filepath.Join(testdata, "plan-create.json"), Against: filepath.Join(testdata, "state.json"),
	})).GetDiff()

	for _, change := range diff.GetResources() {
		t.Errorf("%s changes by %q fixed and %q usage, want no change between a plan and the state it applied", change.GetResource(), change.GetMonthlyFixedDelta(), change.GetMonthlyUsageDelta())
	}
	if diff.GetMonthlyFixedDelta() != "0.00" || diff.GetMonthlyUsageDelta() != "0.00" {
		t.Errorf("totals = %s + %s, want 0.00 + 0.00", diff.GetMonthlyFixedDelta(), diff.GetMonthlyUsageDelta())
	}
}
