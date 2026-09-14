package pgtest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

const (
	URLVariable = "PRICING_TEST_DATABASE_URL"

	lock = 0x0ce1c057
)

func Store(t *testing.T, opts ...postgres.Option) *postgres.Store {
	t.Helper()
	url := hold(t)
	card, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url, card, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.Pool().Exec(ctx, `TRUNCATE aws_products, aws_prices, gcp_skus, ingests`); err != nil {
		t.Fatal(err)
	}
	return store
}

func Empty(t *testing.T) string {
	t.Helper()
	url := hold(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, aws_products, aws_prices, gcp_skus, ingests`); err != nil {
		t.Fatal(err)
	}
	return url
}

func hold(t *testing.T) string {
	t.Helper()
	url := os.Getenv(URLVariable)
	if url == "" {
		t.Skipf("%s names no database", URLVariable)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	held, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock); err != nil {
		held.Release()
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = held.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lock)
		held.Release()
		pool.Close()
	})
	return url
}
