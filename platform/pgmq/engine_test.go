package pgmq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/provider/enginetest"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

func aDatabase(t *testing.T) Config {
	t.Helper()
	server := enginetest.SharedQueueDatabase(t)
	sum := sha256.Sum256([]byte(t.Name()))
	database := "test_" + hex.EncodeToString(sum[:8])
	server.Claim(t, database)
	return Config{ServerURL: server.URL, Database: database}
}

func TestOpeningCreatesTheDedicatedDatabaseWithPgmqAndOpeningAgainKeepsIt(t *testing.T) {
	ctx := context.Background()
	cfg := aDatabase(t)

	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open on a server without the database: %v", err)
	}
	first.Close()
	again, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open on a server that has the database: %v", err)
	}
	defer again.Close()

	conn := connectTo(t, cfg)
	var version string
	if err := conn.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'pgmq'").Scan(&version); err != nil {
		t.Fatalf("read pgmq's version in %s: %v", cfg.Database, err)
	}
	if version != "1.13.0" {
		t.Errorf("pgmq %s in the engine's database, want the 1.13.0 the image ships", version)
	}
}

func TestOpeningRefusesADatabaseWhosePgmqReadsNoHeadPerGroup(t *testing.T) {
	ctx := context.Background()
	cfg := aDatabase(t)
	admin := connectTo(t, Config{ServerURL: cfg.ServerURL})
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cfg.Database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	installed := connectTo(t, cfg)
	if _, err := installed.Exec(ctx, "CREATE EXTENSION pgmq"); err != nil {
		t.Fatal(err)
	}
	if _, err := installed.Exec(ctx, "UPDATE pg_extension SET extversion = '1.10.0' WHERE extname = 'pgmq'"); err != nil {
		t.Fatal(err)
	}

	engine, err := Open(ctx, cfg)
	if err == nil {
		engine.Close()
		t.Fatal("Open took a database with pgmq 1.10.0, whose ordered reads can put two messages of one key in flight")
	}
	if !strings.Contains(err.Error(), "1.11.1") {
		t.Errorf("Open() = %v, want it to name the version an ordered read needs", err)
	}
}

func connectTo(t *testing.T, cfg Config) *pgx.Conn {
	t.Helper()
	parsed, err := pgx.ParseConfig(cfg.ServerURL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "" {
		parsed.Database = cfg.Database
	}
	conn, err := pgx.ConnectConfig(context.Background(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}
