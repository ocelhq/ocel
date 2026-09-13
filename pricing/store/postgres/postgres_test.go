package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

const urlVariable = "PRICING_TEST_DATABASE_URL"

func opened(t *testing.T) *postgres.Store {
	t.Helper()
	url := os.Getenv(urlVariable)
	if url == "" {
		t.Skipf("%s names no database", urlVariable)
	}
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(context.Background(), url, card)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := postgres.Migrate(context.Background(), store.Pool()); err != nil {
		t.Fatalf("Migrate() a second time = %v", err)
	}
	if _, err := store.Pool().Exec(context.Background(), `TRUNCATE aws_products, aws_prices, gcp_skus, ingests`); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAnIngestedPriceWinsOverTheEmbeddedCard(t *testing.T) {
	store := opened(t)
	ctx := context.Background()

	ingest, err := store.IngestAWS(ctx, "AWSLambda", "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	defer ingest.Close()
	if err := ingest.Product(postgres.AWSProduct{SKU: "TESTSKU", Attributes: map[string]string{
		"group": "AWS-Lambda-Requests", "usagetype": "Request", "regionCode": "us-east-1",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ingest.Price(postgres.AWSPrice{SKU: "TESTSKU", Unit: "Requests", BeginRange: decimal.Zero, EndRange: "Inf", Price: decimal.NewFromFloat(0.5)}); err != nil {
		t.Fatal(err)
	}
	if err := ingest.Commit("20260101000000", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	rate, found, fellBack := store.Lookup("aws/lambda/requests", "us-east-1")
	if !found || fellBack {
		t.Fatalf("Lookup() = found %v, fell back %v, want the ingested rate", found, fellBack)
	}
	if len(rate.Tiers) != 1 || !rate.Tiers[0].Price.Equal(decimal.NewFromFloat(0.5)) {
		t.Errorf("tiers = %v, want the ingested price", rate.Tiers)
	}
	if _, version := store.Basis(); version != "2026-01-01" {
		t.Errorf("version = %q, want the day the prices were fetched", version)
	}
}

func TestARateNoIngestAnsweredFallsBackToTheCard(t *testing.T) {
	store := opened(t)
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}

	for id, region := range map[string]string{"aws/cloudfront/requests-https": "", "gcp/firestore/reads": "europe-west1"} {
		want, _, _ := card.Lookup(id, region)
		rate, found, fellBack := store.Lookup(id, region)
		if !found || !fellBack {
			t.Errorf("Lookup(%s) = found %v, fell back %v, want the card's own tiers and the fallback said", id, found, fellBack)
		}
		if !sameTiers(rate.Tiers, want.Tiers) {
			t.Errorf("Lookup(%s) tiers = %v, want the card's %v", id, rate.Tiers, want.Tiers)
		}
	}
}

func TestAVendorWithNoIngestIsAnsweredByItsCardAlone(t *testing.T) {
	store := opened(t)
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}

	want, _, _ := card.Lookup("cloudflare/r2/storage", "")
	rate, found, fellBack := store.Lookup("cloudflare/r2/storage", "")
	if !found || fellBack {
		t.Fatalf("Lookup() = found %v, fell back %v, want the card answering a vendor the store never ingests", found, fellBack)
	}
	if !sameTiers(rate.Tiers, want.Tiers) {
		t.Errorf("tiers = %v, want the card's %v", rate.Tiers, want.Tiers)
	}
}

func TestARateTheCardNeverNamesIsNotFound(t *testing.T) {
	store := opened(t)

	if _, found, _ := store.Lookup("aws/nothing/at-all", "us-east-1"); found {
		t.Error("the store answered for a rate no card names")
	}
}

func sameTiers(got, want []costkit.Tier) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !got[i].Start.Equal(want[i].Start) || !got[i].Price.Equal(want[i].Price) {
			return false
		}
	}
	return true
}
