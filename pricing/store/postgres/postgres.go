package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	VendorAWS = "aws"
	VendorGCP = "gcp"

	basisTimeout = 2 * time.Second

	defaultCacheTTL = 10 * time.Minute

	migrateLock = 0x0ce1c0de
)

//go:embed migrations/*.sql
var migrations embed.FS

type Option func(*Store)

func CacheTTL(held time.Duration) Option {
	return func(s *Store) { s.ttl = held }
}

type held[T any] struct {
	value T
	at    time.Time
}

type answer struct {
	rate     costkit.Rate
	fellBack bool
}

type Store struct {
	pool *pgxpool.Pool
	card *costkit.Card
	ttl  time.Duration

	mu    sync.Mutex
	rates map[string]held[answer]
	basis *held[string]
}

func Open(ctx context.Context, url string, card *costkit.Card, opts ...Option) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("rate store: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	store := &Store{pool: pool, card: card, ttl: defaultCacheTTL, rates: map[string]held[answer]{}}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

func (s *Store) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.rates)
	s.basis = nil
}

func (s *Store) fresh(at time.Time) bool { return time.Since(at) < s.ttl }

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rate store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLock); err != nil {
		return fmt.Errorf("rate store: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("rate store: %w", err)
	}
	for _, name := range names {
		applied, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, name)
		if err != nil {
			return fmt.Errorf("rate store: %s: %w", name, err)
		}
		if applied.RowsAffected() == 0 {
			continue
		}
		statements, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(statements)); err != nil {
			return fmt.Errorf("rate store: %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rate store: %w", err)
	}
	return nil
}

func (s *Store) Basis() (currency, version string) {
	currency, version = s.card.Basis()
	s.mu.Lock()
	cached := s.basis
	s.mu.Unlock()
	if cached != nil && s.fresh(cached.at) {
		return currency, cached.value
	}
	ctx, stop := context.WithTimeout(context.Background(), basisTimeout)
	defer stop()
	var fetched *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(fetched_at) FROM ingests`).Scan(&fetched); err != nil {
		return currency, version
	}
	if fetched != nil {
		version = fetched.UTC().Format(time.DateOnly)
	}
	s.mu.Lock()
	s.basis = &held[string]{value: version, at: time.Now()}
	s.mu.Unlock()
	return currency, version
}

func (s *Store) Lookup(id, region string) (costkit.Rate, bool, bool) {
	key := id + "\x00" + region
	s.mu.Lock()
	cached, known := s.rates[key]
	s.mu.Unlock()
	if known && s.fresh(cached.at) {
		return cached.value.rate, true, cached.value.fellBack
	}
	base, found, fellBack := s.card.Lookup(id, region)
	if !found {
		return base, false, false
	}
	vendor, _, _ := strings.Cut(id, "/")
	if base.Query != nil && base.Query[costkit.QuerySource] == "" && (vendor == VendorAWS || vendor == VendorGCP) {
		tiers, err := s.tiers(vendor, base)
		switch {
		case err != nil || !usable(base, tiers):
			fellBack = true
		default:
			base.Tiers = tiers
		}
	}
	s.mu.Lock()
	s.rates[key] = held[answer]{value: answer{rate: base, fellBack: fellBack}, at: time.Now()}
	s.mu.Unlock()
	return base, true, fellBack
}

func usable(base costkit.Rate, tiers []costkit.Tier) bool {
	if len(tiers) == 0 || !tiers[0].Start.IsZero() {
		return false
	}
	if !slices.IsSortedFunc(tiers, func(a, b costkit.Tier) int { return a.Start.Cmp(b.Start) }) {
		return false
	}
	allows := len(tiers) > 1 && tiers[0].Price.IsZero()
	return allows == (base.Allowance != "")
}

func (s *Store) tiers(vendor string, rate costkit.Rate) ([]costkit.Tier, error) {
	ctx, stop := context.WithTimeout(context.Background(), basisTimeout)
	defer stop()
	if vendor == VendorAWS {
		return s.awsTiers(ctx, rate)
	}
	return s.gcpTiers(ctx, rate)
}

func (s *Store) awsTiers(ctx context.Context, rate costkit.Rate) ([]costkit.Tier, error) {
	region := rate.Region
	if rate.Query[costkit.QueryIndex] == costkit.Global {
		region = ""
	}
	attributes := map[string]string{}
	for key, want := range rate.Query {
		if key == costkit.QueryService || key == costkit.QueryIndex {
			continue
		}
		attributes[key] = want
	}
	selector, err := json.Marshal(attributes)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT sku FROM aws_products WHERE service = $1 AND region = $2 AND attributes @> $3::jsonb`,
		rate.Query[costkit.QueryService], region, string(selector))
	if err != nil {
		return nil, err
	}
	skus, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(skus) != 1 {
		return nil, fmt.Errorf("%s: %d products carry %v in %s", rate.ID, len(skus), attributes, costkit.OrGlobal(region))
	}
	priced, err := s.pool.Query(ctx, `SELECT begin_range, price FROM aws_prices WHERE sku = $1 ORDER BY begin_range`, skus[0])
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(priced, func(row pgx.CollectableRow) (costkit.Tier, error) {
		var start, price decimal.Decimal
		err := row.Scan(&start, &price)
		return costkit.Tier{Start: start, Price: price}, err
	})
}

func (s *Store) gcpTiers(ctx context.Context, rate costkit.Rate) ([]costkit.Tier, error) {
	region := rate.Query[costkit.QueryRegion]
	if region == "" {
		region = costkit.Global
	}
	rows, err := s.pool.Query(ctx, `SELECT tiers FROM gcp_skus WHERE service = $1 AND position($2 in description) > 0 AND $3 = ANY (regions)`,
		rate.Query[costkit.QueryService], rate.Query[costkit.QueryDescription], region)
	if err != nil {
		return nil, err
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	if len(held) != 1 {
		return nil, fmt.Errorf("%s: %d skus describe %q in %s", rate.ID, len(held), rate.Query[costkit.QueryDescription], region)
	}
	var tiers []costkit.Tier
	if err := json.Unmarshal(held[0], &tiers); err != nil {
		return nil, fmt.Errorf("%s: %w", rate.ID, err)
	}
	return tiers, nil
}
