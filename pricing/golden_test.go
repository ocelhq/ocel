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
