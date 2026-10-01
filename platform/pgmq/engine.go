package pgmq

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	Database = "ocel"

	minimumPgmq = "1.11.1"
)

type Config struct {
	ServerURL string
	Database  string
}

type Engine struct {
	pool *pgxpool.Pool

	mu         sync.Mutex
	deployment Deployment
	applied    chan struct{}
	wakes      map[string]chan struct{}
	inFlight   map[string]context.CancelFunc
}

func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.Database == "" {
		cfg.Database = Database
	}
	if err := ensureDatabase(ctx, cfg); err != nil {
		return nil, err
	}
	parsed, err := pgxpool.ParseConfig(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("read the queue database's address: %w", err)
	}
	parsed.ConnConfig.Database = cfg.Database
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("connect to database %s: %w", cfg.Database, err)
	}
	if err := ensureSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Engine{
		pool:     pool,
		applied:  make(chan struct{}, 1),
		wakes:    map[string]chan struct{}{},
		inFlight: map[string]context.CancelFunc{},
	}, nil
}

func (e *Engine) Close() { e.pool.Close() }

func (e *Engine) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, e.pool, fn)
}

func (e *Engine) signalTopic(name string, topic *contractv1.ManifestTopic) {
	for _, consumer := range topic.GetConsumers() {
		e.signal(queueName(name, consumer.GetName()))
	}
}

func (e *Engine) signalApplied() {
	select {
	case e.applied <- struct{}{}:
	default:
	}
}

func (e *Engine) wake(queue string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	wake, found := e.wakes[queue]
	if !found {
		wake = make(chan struct{}, 1)
		e.wakes[queue] = wake
	}
	return wake
}

func (e *Engine) signal(queue string) {
	select {
	case e.wake(queue) <- struct{}{}:
	default:
	}
}

func ensureDatabase(ctx context.Context, cfg Config) error {
	conn, err := pgx.Connect(ctx, cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("connect to the queue database's server: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", cfg.Database).Scan(&exists); err != nil {
		return fmt.Errorf("look for database %s: %w", cfg.Database, err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cfg.Database}.Sanitize()); err != nil {
		return fmt.Errorf("create database %s: %w", cfg.Database, err)
	}
	return nil
}

func ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgmq"); err != nil {
		return fmt.Errorf("install pgmq: %w", err)
	}
	var version string
	if err := pool.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'pgmq'").Scan(&version); err != nil {
		return fmt.Errorf("read pgmq's version: %w", err)
	}
	if !isAtLeast(version, minimumPgmq) {
		return fmt.Errorf("the queue database runs pgmq %s, and an ordered consumer needs read_grouped_head from pgmq %s or later: run ALTER EXTENSION pgmq UPDATE", version, minimumPgmq)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("create the engine's tables: %w", err)
	}
	return nil
}

func isAtLeast(version, minimum string) bool {
	have, want := strings.Split(version, "."), strings.Split(minimum, ".")
	for i := range want {
		if i >= len(have) {
			return false
		}
		h, _ := strconv.Atoi(have[i])
		w, _ := strconv.Atoi(want[i])
		if h != w {
			return h > w
		}
	}
	return true
}
