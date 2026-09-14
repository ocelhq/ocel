package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/internal/pgtest"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

func TestAnIngestedPriceWinsOverTheEmbeddedCard(t *testing.T) {
	store := pgtest.Store(t)
	lambdaRequests(t, store, 0.5)

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
	store := pgtest.Store(t)
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
	store := pgtest.Store(t)
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
	store := pgtest.Store(t)

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

func lambdaRequests(t *testing.T, store *postgres.Store, price float64) {
	t.Helper()
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
	if err := ingest.Price(postgres.AWSPrice{SKU: "TESTSKU", Unit: "Requests", BeginRange: decimal.Zero, EndRange: "Inf", Price: decimal.NewFromFloat(price)}); err != nil {
		t.Fatal(err)
	}
	if err := ingest.Commit("20260101000000", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func behindTheStoresBack(t *testing.T, store *postgres.Store, price float64) {
	t.Helper()
	if _, err := store.Pool().Exec(context.Background(), `UPDATE aws_prices SET price = $1 WHERE sku = 'TESTSKU'`, price); err != nil {
		t.Fatal(err)
	}
}

func requestPrice(t *testing.T, store *postgres.Store) decimal.Decimal {
	t.Helper()
	rate, found, _ := store.Lookup("aws/lambda/requests", "us-east-1")
	if !found {
		t.Fatal("the card names no aws/lambda/requests")
	}
	if len(rate.Tiers) != 1 {
		t.Fatalf("tiers = %v, want the single ingested tier", rate.Tiers)
	}
	return rate.Tiers[0].Price
}

func TestARateAlreadyLookedUpIsNotReadFromThePriceStoreAgain(t *testing.T) {
	store := pgtest.Store(t)
	lambdaRequests(t, store, 0.5)
	if got := requestPrice(t, store); !got.Equal(decimal.NewFromFloat(0.5)) {
		t.Fatalf("price = %s, want the ingested 0.5", got)
	}

	behindTheStoresBack(t, store, 0.9)

	if got := requestPrice(t, store); !got.Equal(decimal.NewFromFloat(0.5)) {
		t.Errorf("price = %s, want the rate it already read rather than a second round trip", got)
	}
}

func TestAnIngestLetsGoOfWhatTheStoreHeld(t *testing.T) {
	store := pgtest.Store(t)
	lambdaRequests(t, store, 0.5)
	requestPrice(t, store)

	lambdaRequests(t, store, 0.9)

	if got := requestPrice(t, store); !got.Equal(decimal.NewFromFloat(0.9)) {
		t.Errorf("price = %s, want the price the second ingest wrote", got)
	}
}

func TestARateReadLongerAgoThanTheTTLIsReadAgain(t *testing.T) {
	store := pgtest.Store(t, postgres.CacheTTL(time.Nanosecond))
	lambdaRequests(t, store, 0.5)
	requestPrice(t, store)

	behindTheStoresBack(t, store, 0.9)

	if got := requestPrice(t, store); !got.Equal(decimal.NewFromFloat(0.9)) {
		t.Errorf("price = %s, want a rate held no longer than the ttl allows", got)
	}
}
