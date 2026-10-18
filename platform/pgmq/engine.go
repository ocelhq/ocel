package pgmq

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	Database = "ocel"

	defaultLease = 60 * time.Second
	maxInFlight  = 1000

	minimumPgmq = "1.11.1"
	openLock    = "ocel.pgmq.open"
)

type Config struct {
	ServerURL     string
	Database      string
	Lease         time.Duration
	ShutdownGrace time.Duration
	ReportAttempt func(runs.Attempt)
}

type Engine struct {
	pool *pgxpool.Pool
	runs *runs.Engine

	mu         sync.Mutex
	deployment Deployment
	applied    chan struct{}
	wakes      map[string]chan struct{}
	turns      map[string]*laneTurns
}

func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.Database == "" {
		cfg.Database = Database
	}
	if cfg.Lease <= 0 {
		cfg.Lease = defaultLease
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
	e := &Engine{
		pool:    pool,
		applied: make(chan struct{}, 1),
		wakes:   map[string]chan struct{}{},
		turns:   map[string]*laneTurns{},
	}
	e.runs = runs.New(runs.Config{
		Store:         e.Store(),
		Queue:         queue{engine: e},
		Lease:         cfg.Lease,
		ShutdownGrace: cfg.ShutdownGrace,
		InFlight:      maxInFlight,
		ReportAttempt: cfg.ReportAttempt,
	})
	return e, nil
}

func (e *Engine) Close() { e.pool.Close() }

func (e *Engine) Tasks() runs.Tasks { return e.runs.Tasks() }

func (e *Engine) Topics() runs.Topics { return e.runs.Topics() }

func (e *Engine) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, e.pool, fn)
}

func (e *Engine) signalApplied() {
	select {
	case e.applied <- struct{}{}:
	default:
	}
}

func (e *Engine) ensureWakeChannel(queue string) chan struct{} {
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
	case e.ensureWakeChannel(queue) <- struct{}{}:
	default:
	}
}

func (e *Engine) laneTurnsOf(queue string) *laneTurns {
	e.mu.Lock()
	defer e.mu.Unlock()
	turns, found := e.turns[queue]
	if !found {
		turns = &laneTurns{}
		e.turns[queue] = turns
	}
	return turns
}

func ensureDatabase(ctx context.Context, cfg Config) error {
	conn, err := pgx.Connect(ctx, cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("connect to the queue database's server: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", openLock); err != nil {
		return fmt.Errorf("wait for other engines creating databases: %w", err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", cfg.Database).Scan(&exists); err != nil {
		return fmt.Errorf("look for database %s: %w", cfg.Database, err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cfg.Database}.Sanitize()); err != nil && !isDuplicateDatabase(err) {
		return fmt.Errorf("create database %s: %w", cfg.Database, err)
	}
	return nil
}

func isDuplicateDatabase(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P04"
}

func ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", openLock); err != nil {
			return fmt.Errorf("wait for other engines creating the schema: %w", err)
		}
		if _, err := tx.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgmq"); err != nil {
			return fmt.Errorf("install pgmq: %w", err)
		}
		var version string
		if err := tx.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'pgmq'").Scan(&version); err != nil {
			return fmt.Errorf("read pgmq's version: %w", err)
		}
		if !isAtLeast(version, minimumPgmq) {
			return fmt.Errorf("the queue database runs pgmq %s, and an ordered consumer needs read_grouped_head from pgmq %s or later: run ALTER EXTENSION pgmq UPDATE", version, minimumPgmq)
		}
		if _, err := tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("create the engine's tables: %w", err)
		}
		return nil
	})
}

func isAtLeast(version, minimum string) bool {
	have, want := strings.Split(version, "."), strings.Split(minimum, ".")
	for i := range want {
		if i >= len(have) {
			return false
		}
		haveNumber, _ := strconv.Atoi(have[i])
		wantNumber, _ := strconv.Atoi(want[i])
		if haveNumber != wantNumber {
			return haveNumber > wantNumber
		}
	}
	return true
}
