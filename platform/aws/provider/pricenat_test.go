package aws_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func TestANATGatewayIsPricedPerHourAndPerGBItProcesses(t *testing.T) {
	_, costs := costServed(t)

	properties, _ := structpb.NewStruct(map[string]any{})
	set := &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{
			{Id: "p/aws_nat_gateway:functions", Scope: "p", Vendor: "aws", Type: "aws_nat_gateway", Name: "functions", Region: "us-east-1", Properties: properties},
			{Id: "p/aws_eip:functions", Scope: "p", Vendor: "aws", Type: "aws_eip", Name: "functions", Region: "us-east-1", Properties: properties},
		},
	}
	est, err := costs.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}

	if got := estimateNamed(t, est, "p/aws_nat_gateway:functions").GetMonthlyFixed(); got != "32.85" {
		t.Errorf("NAT gateway fixed = %s, want 32.85 (730 h at 0.045)", got)
	}
	processed := componentNamed(t, est, "p/aws_nat_gateway:functions", "Data processed")
	if processed.GetUnitPrice() != "0.045" || processed.GetMonthlyQuantity() != "10" {
		t.Errorf("data processed = %s GB at %s, want the moderate profile's 10 GB at 0.045", processed.GetMonthlyQuantity(), processed.GetUnitPrice())
	}
	if got := estimateNamed(t, est, "p/aws_eip:functions").GetMonthlyFixed(); got != "3.65" {
		t.Errorf("elastic IP fixed = %s, want 3.65 (730 h of a public IPv4 address at 0.005)", got)
	}
}
