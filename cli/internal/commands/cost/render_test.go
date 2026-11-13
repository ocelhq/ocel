package cost

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func TestARenderedScanShowsEveryResourceOnceWhateverItsScopesClaim(t *testing.T) {
	set := &costv1.ResourceSet{
		Scopes: []*costv1.Scope{
			{Id: "app", Kind: "app", Name: "web"},
			{Id: "app", Parent: "app", Kind: "app", Name: "web again"},
			{Id: "", Kind: "unnamed"},
			{Id: "left", Parent: "right", Kind: "app", Name: "left"},
			{Id: "right", Parent: "left", Kind: "app", Name: "right"},
		},
		Resources: []*costv1.Resource{
			{Id: "fn", Scope: "app", Type: "fake_function", Name: "in-app"},
			{Id: "loose", Type: "fake_function", Name: "at-root"},
			{Id: "l", Scope: "left", Type: "fake_function", Name: "in-left"},
			{Id: "r", Scope: "right", Type: "fake_function", Name: "in-right"},
		},
	}
	estimates := byProfile{pricing.DefaultProfile: &costv1.Estimate{Currency: "USD"}}

	var out bytes.Buffer
	if err := render(&out, "scan", set, estimates, pricing.DefaultProfile, nil); err != nil {
		t.Fatalf("render() = %v", err)
	}

	for _, name := range []string{"in-app", "at-root", "in-left", "in-right"} {
		if n := strings.Count(out.String(), name); n != 1 {
			t.Errorf("%s shown %d times, want once:\n%s", name, n, out.String())
		}
	}
}

func TestARenderedDiffShowsAChangeItCannotPriceAsUnknown(t *testing.T) {
	set := &costv1.ResourceSet{Resources: []*costv1.Resource{{Id: "svc", Type: "aws_ecs_service"}}}
	diffs := map[costv1.Profile]*costv1.EstimateDiff{}
	for _, each := range pricing.Profiles() {
		diffs[each] = &costv1.EstimateDiff{
			Current: &costv1.Estimate{Currency: "USD"}, Proposed: &costv1.Estimate{Currency: "USD"},
			MonthlyFixedDelta: "0.00", MonthlyUsageDelta: "0.00",
			Resources: []*costv1.ResourceChange{{Resource: "svc", Change: costv1.ResourceChange_CHANGE_MODIFIED, MonthlyUsageDelta: "0.00"}},
		}
	}

	var out bytes.Buffer
	if err := renderDiff(&out, "diff", set, set, diffs, pricing.DefaultProfile, nil); err != nil {
		t.Fatalf("renderDiff() = %v", err)
	}

	row := strings.Fields(strings.Split(out.String(), "\n")[3])
	if want := []string{"svc", "aws_ecs_service", "changed", "unknown", "+0.00"}; !slices.Equal(row, want) {
		t.Errorf("row = %v, want %v:\n%s", row, want, out.String())
	}
}
