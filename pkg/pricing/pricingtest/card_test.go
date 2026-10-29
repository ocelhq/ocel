package pricingtest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/ingest"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
)

func product(sku, region string, attributes string) string {
	return fmt.Sprintf(`{"service": "AWSLambda", "sku": %q, "region": %q, "productFamily": "Serverless", "attributes": %s,
		"purchaseOption": "on_demand", "unit": "Requests", "tiers": [{"start": "0", "price": "0.0000002"}],
		"source": "https://example.test/%s", "verified": "2026-10-22"}`, sku, region, attributes, sku)
}

func card(t *testing.T, products ...string) *pricingtest.Card {
	t.Helper()
	loaded, err := pricingtest.NewCard([]byte(`{"format": 1, "vendor": "aws", "version": "20261022183959", "currency": "USD", "products": [` +
		strings.Join(products, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func lookup(t *testing.T, c *pricingtest.Card, query pricing.Query) pricing.Answer {
	t.Helper()
	answers, err := c.Lookup(context.Background(), []pricing.Query{query})
	if err != nil || len(answers) != 1 {
		t.Fatalf("Lookup() = %v, %v", answers, err)
	}
	return answers[0]
}

func TestACardAnswersTheOneProductWhoseAttributesHoldEveryQueriedOne(t *testing.T) {
	c := card(t,
		product("REQ", "eu-west-2", `{"group": "AWS-Lambda-Requests", "usagetype": "Request"}`),
		product("ARM", "eu-west-2", `{"group": "AWS-Lambda-Requests-ARM", "usagetype": "Request-ARM"}`))

	got := lookup(t, c, pricing.Query{Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2", Attributes: map[string]string{"usagetype": "Request"}})

	if got.Kind != pricing.AnswerPriced || got.Price.SKU != "REQ" || got.Price.CatalogVersion != "20261022183959" || got.Price.Source != "https://example.test/REQ" {
		t.Errorf("answer = %+v, want REQ priced at the card's version", got)
	}
}

func TestACardAnswersAmbiguityWithEveryCandidateInOrder(t *testing.T) {
	c := card(t,
		product("B", "us-east-1", `{"usagetype": "DataProcessing-Bytes", "version": "Legacy"}`),
		product("A", "us-east-1", `{"usagetype": "DataProcessing-Bytes", "version": "Current"}`))

	got := lookup(t, c, pricing.Query{Vendor: "aws", Service: "AWSLambda", Region: "us-east-1", Attributes: map[string]string{"usagetype": "DataProcessing-Bytes"}})

	if got.Kind != pricing.AnswerAmbiguous || strings.Join(got.Candidates, ",") != "A,B" || got.Matches != 2 {
		t.Errorf("answer = %+v, want an ambiguity naming A and B", got)
	}
}

func TestACardMissesAnotherRegionAnUnseededServiceAndAnotherUnit(t *testing.T) {
	c := card(t, product("REQ", "eu-west-2", `{"usagetype": "Request"}`))

	for name, query := range map[string]pricing.Query{
		"region":  {Vendor: "aws", Service: "AWSLambda", Region: "us-east-1", Attributes: map[string]string{"usagetype": "Request"}},
		"service": {Vendor: "aws", Service: "AmazonSQS", Region: "eu-west-2"},
		"unit":    {Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2", Unit: "GB"},
		"family":  {Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2", ProductFamily: "Storage"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := lookup(t, c, query); got.Kind != pricing.AnswerMissing || got.Reason == "" {
				t.Errorf("answer = %+v, want a miss with its reason", got)
			}
		})
	}
	if got := lookup(t, c, pricing.Query{Vendor: "gcp", Service: "152E-C115-5142"}); !strings.Contains(got.Reason, "seeded") {
		t.Errorf("reason = %q, want it to say the catalog is not seeded", got.Reason)
	}
}

func TestACardRecordsEveryLookup(t *testing.T) {
	c := card(t, product("REQ", "eu-west-2", `{"usagetype": "Request"}`))
	query := pricing.Query{Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2"}

	lookup(t, c, query)
	lookup(t, c, query)

	if got := c.Lookups(); len(got) != 2 || len(got[0]) != 1 {
		t.Errorf("lookups = %v, want two of one query each", got)
	}
}

func TestACardNamesWhatAVendorsIngestWouldMissOfWhatWasAskedFor(t *testing.T) {
	c := card(t, product("REQ", "eu-west-2", `{"usagetype": "Request", "locationType": "AWS Local Zone"}`))
	lookup(t, c, pricing.Query{Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2"})
	lookup(t, c, pricing.Query{Vendor: "aws", Service: "AmazonSQS", Region: "eu-west-2"})

	missed := c.MissedBy(ingest.Vendor{Vendor: "aws", Services: []ingest.Service{
		{Service: "AWSLambda", Exclude: []ingest.Filter{{Attribute: "locationType", In: []string{"AWS Local Zone"}}}},
	}})

	if strings.Join(missed, "|") != "aws AWSLambda drops sku REQ|aws ingests no service AmazonSQS" {
		t.Errorf("missed = %v", missed)
	}
}

func TestACardRefusesACatalogTheStaticReaderRefuses(t *testing.T) {
	if _, err := pricingtest.NewCard([]byte(`{"format": 1, "vendor": "aws", "version": "v", "currency": "USD", "products": [
		{"service": "S", "sku": "A", "region": "", "purchaseOption": "on_demand", "unit": "h", "tiers": [], "source": "s", "verified": "d"}]}`)); err == nil {
		t.Fatal("NewCard() accepted a product with no tiers")
	}
}

func TestACardAnswersFromEachVendorsCatalog(t *testing.T) {
	c, err := pricingtest.NewCard(
		[]byte(`{"format": 1, "vendor": "aws", "version": "a1", "currency": "USD", "products": [`+product("REQ", "eu-west-2", `{}`)+`]}`),
		[]byte(`{"format": 1, "vendor": "cloudflare", "version": "c1", "currency": "USD", "products": [
			{"service": "workers", "sku": "W", "region": "", "purchaseOption": "on_demand", "unit": "requests",
			 "tiers": [{"start": "0", "price": "1"}], "source": "s", "verified": "d"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	aws := lookup(t, c, pricing.Query{Vendor: "aws", Service: "AWSLambda", Region: "eu-west-2"})
	workers := lookup(t, c, pricing.Query{Vendor: "cloudflare", Service: "workers"})
	if aws.Price.SKU != "REQ" || aws.Price.CatalogVersion != "a1" || workers.Price.SKU != "W" || workers.Price.CatalogVersion != "c1" {
		t.Errorf("answers = %+v, %+v; want each priced at its own catalog's version", aws, workers)
	}
}
