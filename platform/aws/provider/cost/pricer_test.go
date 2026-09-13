package cost_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/cost"
)

func set() *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{
			{Id: "p/aws_s3_bucket:uploads", Scope: "p", Vendor: cost.Vendor, Type: "aws_s3_bucket", Name: "uploads", Region: "us-east-1"},
		},
	}
}

func TestThePartsPriceWhatPriceDoes(t *testing.T) {
	card, err := cost.Card()
	if err != nil {
		t.Fatal(err)
	}
	if cost.Vendor != "aws" {
		t.Errorf("Vendor = %q", cost.Vendor)
	}
	if _, prices := cost.Table["aws_s3_bucket"]; !prices {
		t.Error("the table prices no bucket")
	}
	if len(cost.Notes) == 0 {
		t.Error("the pricer publishes no provenance notes")
	}

	whole, err := cost.Price(&costv1.PriceRequest{Resources: set()})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := costkit.Estimate(card, cost.Table, &costv1.PriceRequest{Resources: set()})
	if err != nil {
		t.Fatal(err)
	}
	if whole.GetMonthlyUsage() != parts.GetMonthlyUsage() || whole.GetRatesVersion() != parts.GetRatesVersion() {
		t.Errorf("Price() = %s at %s, the parts price %s at %s", whole.GetMonthlyUsage(), whole.GetRatesVersion(), parts.GetMonthlyUsage(), parts.GetRatesVersion())
	}
	for _, note := range cost.Notes {
		if !slices.Contains(whole.GetNotes(), note) {
			t.Errorf("Price() drops the note %q", note)
		}
	}
}

func vpc() *costv1.ResourceSet {
	held := func(id, typ string, properties map[string]any) *costv1.Resource {
		fields, err := costkit.Struct(properties)
		if err != nil {
			panic(err)
		}
		return &costv1.Resource{Id: "p/" + id, Scope: "p", Vendor: cost.Vendor, Type: typ, Name: id, Region: "us-east-1", Properties: fields}
	}
	return &costv1.ResourceSet{
		Source: "sst",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "stage", Name: "dev"}},
		Resources: []*costv1.Resource{
			held("vpc", "aws_vpc", nil),
			held("subnet", "aws_subnet", nil),
			held("group", "aws_security_group", nil),
			held("table", "aws_route_table", nil),
			held("gateway", "aws_internet_gateway", nil),
			held("nat", "aws_nat_gateway", nil),
			held("address", "aws_eip", nil),
			held("s3", "aws_vpc_endpoint", map[string]any{"vpc_endpoint_type": "Gateway"}),
			held("secrets", "aws_vpc_endpoint", map[string]any{
				"vpc_endpoint_type": "Interface",
				"subnet_ids":        []any{"subnet-a", "subnet-b", "subnet-c"},
			}),
		},
	}
}

func priced(t *testing.T, est *costv1.Estimate, id string) *costv1.ResourceEstimate {
	t.Helper()
	for _, r := range est.GetResources() {
		if r.GetResource() == id {
			return r
		}
	}
	t.Fatalf("no estimate for %s", id)
	return nil
}

func TestTheVPCsFreePiecesAreFreeAndItsPaidOnesAreNot(t *testing.T) {
	est, err := cost.Price(&costv1.PriceRequest{Resources: vpc()})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	if est.GetCoverage().GetUnsupported() != 0 {
		t.Fatalf("the pricer does not recognise %v", est.GetCoverage().GetUnsupportedTypes())
	}

	for _, id := range []string{"p/vpc", "p/subnet", "p/group", "p/table", "p/gateway", "p/s3"} {
		if got := priced(t, est, id).GetStatus(); got != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s is %s, want free", id, got)
		}
	}
	for _, id := range []string{"p/nat", "p/address", "p/secrets"} {
		if got := priced(t, est, id).GetStatus(); got != costv1.ResourceEstimate_STATUS_PRICED {
			t.Errorf("%s is %s, want a price", id, got)
		}
	}
}

func TestAnInterfaceEndpointIsPricedPerAvailabilityZone(t *testing.T) {
	est, err := cost.Price(&costv1.PriceRequest{Resources: vpc()})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}

	one := priced(t, est, "p/nat").GetComponents()[0]
	three := priced(t, est, "p/secrets").GetComponents()[0]
	if one.GetMonthlyQuantity() != "730" {
		t.Errorf("the NAT gateway stands for %s hours, want a whole month", one.GetMonthlyQuantity())
	}
	if three.GetMonthlyQuantity() != "2190" {
		t.Errorf("the interface endpoint stands for %s hours, want a month in each of its three subnets", three.GetMonthlyQuantity())
	}
	if data := priced(t, est, "p/nat").GetComponents()[1]; !data.GetUsageBased() || data.GetMonthlyCost() == "" {
		t.Errorf("the NAT gateway processes no data: %v", data)
	}
}
