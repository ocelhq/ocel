package pgtest

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

const (
	URLVariable = "PRICING_TEST_DATABASE_URL"

	lock = 0x0ce1c057
)

func Store(t *testing.T) *postgres.Store {
	t.Helper()
	url := os.Getenv(URLVariable)
	if url == "" {
		t.Skipf("%s names no database", URLVariable)
	}
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url, card)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	held, err := store.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = held.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lock)
		held.Release()
	})
	if _, err := store.Pool().Exec(ctx, `TRUNCATE aws_products, aws_prices, gcp_skus, ingests`); err != nil {
		t.Fatal(err)
	}
	return store
}
