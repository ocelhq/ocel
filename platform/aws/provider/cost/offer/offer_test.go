package offer_test

import (
	"os"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/cost/offer"
)

func TestReadOnDemandKeepsTheOnDemandTermAndWalksPastTheRest(t *testing.T) {
	file, err := os.Open("testdata/offer.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	products := map[string]map[string]string{}
	prices := map[string]offer.Price{}
	version, err := offer.ReadOnDemand(file, offer.Sink{
		Product: func(p offer.Product) error {
			products[p.SKU] = p.Attributes
			return nil
		},
		Price: func(p offer.Price) error {
			prices[p.SKU] = p
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ReadOnDemand() = %v", err)
	}

	if version != "20260911124513" {
		t.Errorf("version = %q", version)
	}
	if len(products) != 2 || products["NATSKU"]["usagetype"] != "NatGateway-Hours" {
		t.Errorf("products = %v", products)
	}
	if len(prices) != 2 {
		t.Fatalf("prices = %v, want one per sku and nothing from the reserved term", prices)
	}
	if got := prices["NATSKU"]; got.USD != "0.0450000000" || got.Unit != "Hrs" || got.EndRange != "Inf" {
		t.Errorf("the NAT gateway prices at %v, want the on-demand hour, not the reserved quantity", got)
	}
}

func TestReadOnDemandStopsWhereTheSinkStops(t *testing.T) {
	file, err := os.Open("testdata/offer.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	seen := 0
	if _, err := offer.ReadOnDemand(file, offer.Sink{Product: func(offer.Product) error {
		seen++
		return os.ErrClosed
	}}); err == nil {
		t.Fatal("ReadOnDemand() swallowed the sink's refusal")
	}
	if seen != 1 {
		t.Errorf("the sink saw %d products after refusing the first", seen)
	}
}

func TestPathAndSourceNameTheRegionalAndGlobalIndexes(t *testing.T) {
	if got := offer.Path("AmazonVPC", "us-east-1"); got != "/offers/v1.0/aws/AmazonVPC/current/us-east-1/index.json" {
		t.Errorf("Path() = %s", got)
	}
	if got := offer.Path("AmazonVPC", ""); got != "/offers/v1.0/aws/AmazonVPC/current/index.json" {
		t.Errorf("Path() = %s", got)
	}
	if got := offer.Source("AmazonVPC", "2026", ""); got != offer.Host+"/offers/v1.0/aws/AmazonVPC/2026/global/index.json" {
		t.Errorf("Source() = %s", got)
	}
}
