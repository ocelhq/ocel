package pricing_test

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	aws "github.com/ocelhq/ocel/platform/aws/provider/cost"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

const pricedTokens = "../cli/internal/costsource/pulumi/testdata/priced_tokens.txt"

var declaredByNoTool = []string{"aws_data_transfer"}

func TestTheForeignSourcesKnowEveryTypeThisServicePrices(t *testing.T) {
	var want []string
	for _, vendor := range []struct {
		name  string
		table costkit.Table
	}{{aws.Vendor, aws.Table}, {gcp.Vendor, gcp.Table}, {cloudflare.Vendor, cloudflare.Table}} {
		for _, typ := range slices.Sorted(maps.Keys(vendor.table)) {
			if !slices.Contains(declaredByNoTool, typ) {
				want = append(want, vendor.name+" "+typ)
			}
		}
	}
	slices.Sort(want)

	raw, err := os.ReadFile(pricedTokens)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%q is not a vendor, a priced type and the pulumi token for it", line)
		}
		got = append(got, fields[0]+" "+fields[1])
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Errorf("%s has drifted from what this service prices:\n missing %v\n stale %v", pricedTokens, missing(want, got), missing(got, want))
	}
}

func missing(want, got []string) []string {
	var out []string
	for _, held := range want {
		if !slices.Contains(got, held) {
			out = append(out, held)
		}
	}
	return out
}
