package ingest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/ingest"
	"github.com/ocelhq/ocel/pricing/internal/pgtest"
)

func offers(t *testing.T) *httptest.Server {
	t.Helper()
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/offers/v1.0/aws/AWSLambda/current/us-east-1/index.json" {
			http.NotFound(w, r)
			return
		}
		asked++
		if asked == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		http.ServeFile(w, r, "testdata/lambda_offer.json")
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTheLambdaOfferPricesThroughTheStore(t *testing.T) {
	store := pgtest.Store(t)
	server := offers(t)
	at := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)

	loader := ingest.AWS{Store: store, Host: server.URL, Now: func() time.Time { return at }}
	if err := loader.Run(context.Background(), "AWSLambda", "us-east-1"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	rate, found, fellBack := store.Lookup("aws/lambda/requests", "us-east-1")
	if !found || fellBack {
		t.Fatalf("Lookup() = found %v, fell back %v", found, fellBack)
	}
	if len(rate.Tiers) != 1 || !rate.Tiers[0].Price.Equal(decimal.RequireFromString("0.0000009")) {
		t.Errorf("tiers = %v, want the ingested on-demand request price", rate.Tiers)
	}
	duration, _, _ := store.Lookup("aws/lambda/duration", "us-east-1")
	if len(duration.Tiers) != 2 || !duration.Tiers[1].Start.Equal(decimal.RequireFromString("6000000000")) {
		t.Errorf("tiers = %v, want both price dimensions in ascending order", duration.Tiers)
	}
	if _, version := store.Basis(); version != "2026-02-03" {
		t.Errorf("version = %q, want the day the offer was fetched", version)
	}
}

func TestRunningTheSameSliceTwiceReplacesIt(t *testing.T) {
	store := pgtest.Store(t)
	server := offers(t)
	loader := ingest.AWS{Store: store, Host: server.URL}

	for range 2 {
		if err := loader.Run(context.Background(), "AWSLambda", "us-east-1"); err != nil {
			t.Fatalf("Run() = %v", err)
		}
	}

	var products, prices int
	row := store.Pool().QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM aws_products), (SELECT count(*) FROM aws_prices)`)
	if err := row.Scan(&products, &prices); err != nil {
		t.Fatal(err)
	}
	if products != 2 || prices != 3 {
		t.Errorf("the store holds %d products and %d prices, want the one slice the offer carries", products, prices)
	}
}

func TestTheFirestoreCatalogPricesThroughTheStore(t *testing.T) {
	store := pgtest.Store(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "a-key" {
			http.Error(w, "no key", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, "testdata/firestore_skus.json")
	}))
	t.Cleanup(server.Close)

	loader := ingest.GCP{Store: store, Host: server.URL, Key: "a-key"}
	if err := loader.Run(context.Background(), "EE2C-7FAC-5E08"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	rate, found, fellBack := store.Lookup("gcp/firestore/reads", "europe-west1")
	if !found || fellBack {
		t.Fatalf("Lookup() = found %v, fell back %v", found, fellBack)
	}
	if len(rate.Tiers) != 2 || !rate.Tiers[1].Price.Equal(decimal.RequireFromString("0.0000005")) {
		t.Errorf("tiers = %v, want both of the catalog's tiered rates", rate.Tiers)
	}
}

func TestTheTargetsComeFromTheCardsOwnQueries(t *testing.T) {
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}

	targets := ingest.AWSTargets(card)
	if len(targets) == 0 {
		t.Fatal("the card names no AWS offer file to ingest")
	}
	for _, target := range targets {
		if target.Service == "" {
			t.Errorf("target %v names no service", target)
		}
	}
	if !hasTarget(targets, ingest.AWSTarget{Service: "AWSLambda", Region: "us-east-1"}) {
		t.Errorf("targets = %v, want the Lambda offer the card's own rates query", targets)
	}
	if services := ingest.GCPServices(card); len(services) == 0 {
		t.Error("the card names no GCP service to ingest")
	}
}

func hasTarget(targets []ingest.AWSTarget, want ingest.AWSTarget) bool {
	for _, target := range targets {
		if target == want {
			return true
		}
	}
	return false
}
