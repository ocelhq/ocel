package pricing_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/google/go-cmp/cmp"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	aws "github.com/ocelhq/ocel/platform/aws/provider/cost"
	"github.com/ocelhq/ocel/pricing"
)

const awsGoldens = "../platform/aws/provider/testdata"

func awsGolden[T proto.Message](t *testing.T, name string, into T) T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(awsGoldens, name+".golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
	return into
}

func TestTheServiceAnswersTheCostConformanceChecks(t *testing.T) {
	_, pricer := served(t, pricing.Options{})

	conformance.RunPricing(t, pricer, awsGolden(t, "inventory_production_cloudfront", &costv1.ResourceSet{}))
}

func TestTheServicePricesAnInventoryExactlyAsTheProviderDoes(t *testing.T) {
	_, pricer := served(t, pricing.Options{})
	set := awsGolden(t, "inventory_production_cloudfront", &costv1.ResourceSet{})
	want := awsGolden(t, "estimate_production_cloudfront", &costv1.Estimate{})

	got, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}

	for _, note := range aws.Notes {
		if !slices.Contains(got.GetNotes(), note) {
			t.Errorf("the service drops the AWS note %q", note)
		}
	}
	got.Notes, want.Notes = nil, nil
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("the service and the provider disagree about the same inventory (-provider +service):\n%s", diff)
	}
}

const sstInventory = "../cli/internal/costsource/pulumi/testdata/sst.golden.json"

func TestAnSSTStageParsedByTheCLIPricesEndToEnd(t *testing.T) {
	raw, err := os.ReadFile(sstInventory)
	if err != nil {
		t.Fatal(err)
	}
	var set costv1.ResourceSet
	if err := protojson.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	_, pricer := served(t, pricing.Options{})

	estimate, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: &set, Usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_MODERATE}})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}

	coverage := estimate.GetCoverage()
	if coverage.GetUnsupported() != 0 {
		t.Errorf("coverage = %v, want every type the sst stage stands up priced or free", coverage)
	}
	byResource := map[string]*costv1.ResourceEstimate{}
	for _, held := range estimate.GetResources() {
		byResource[held.GetResource()] = held
	}
	nat := byResource["urn:pulumi:victor::with-sst::sst:aws:Vpc$aws:ec2/natGateway:NatGateway::Vpc NatGateway1"]
	if nat.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED || nat.GetMonthlyFixed() == "" || nat.GetMonthlyFixed() == "0" {
		t.Errorf("the NAT gateway = %v, want the hours it stands charged whatever the traffic", nat)
	}
	gateway := byResource["urn:pulumi:victor::with-sst::aws:ec2/vpcEndpoint:VpcEndpoint::Kms"]
	if gateway.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
		t.Errorf("the interface endpoint = %v, want it priced per availability zone", gateway)
	}
	if estimate.GetMonthlyFixed() == "" || estimate.GetMonthlyFixed() == "0.00" {
		t.Errorf("monthly fixed = %q, want the stage's standing charges", estimate.GetMonthlyFixed())
	}
}
