package postgres_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ocelhq/ocel/pricing/internal/pgtest"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

func TestMigratorsThatStartTogetherAllSucceed(t *testing.T) {
	url := pgtest.Empty(t)
	ctx := context.Background()

	starters := make([]error, 8)
	start := make(chan struct{})
	var running sync.WaitGroup
	for i := range starters {
		running.Add(1)
		go func() {
			defer running.Done()
			pool, err := pgxpool.New(ctx, url)
			if err != nil {
				starters[i] = err
				return
			}
			defer pool.Close()
			<-start
			starters[i] = postgres.Migrate(ctx, pool)
		}()
	}
	close(start)
	running.Wait()

	for i, err := range starters {
		if err != nil {
			t.Errorf("Migrate() from starter %d = %v, want every starter to succeed", i, err)
		}
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, table := range []string{"schema_migrations", "aws_products", "aws_prices", "gcp_skus", "ingests"} {
		var name *string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == nil {
			t.Errorf("to_regclass(%q) = null, want the migrated table", table)
		}
	}
}
