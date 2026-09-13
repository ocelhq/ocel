package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const batchSize = 2000

type AWSProduct struct {
	SKU        string
	Attributes map[string]string
}

type AWSPrice struct {
	SKU        string
	Unit       string
	BeginRange decimal.Decimal
	EndRange   string
	Price      decimal.Decimal
}

type GCPSKU struct {
	ID          string
	Service     string
	Description string
	Category    map[string]string
	Regions     []string
	Tiers       []costkit.Tier
}

type Ingest struct {
	ctx     context.Context
	tx      pgx.Tx
	batch   *pgx.Batch
	vendor  string
	service string
	region  string
}

func (s *Store) IngestAWS(ctx context.Context, service, region string) (*Ingest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aws_prices WHERE sku IN (SELECT sku FROM aws_products WHERE service = $1 AND region = $2)`, service, region); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aws_products WHERE service = $1 AND region = $2`, service, region); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &Ingest{ctx: ctx, tx: tx, batch: &pgx.Batch{}, vendor: VendorAWS, service: service, region: region}, nil
}

func (s *Store) IngestGCP(ctx context.Context, service string) (*Ingest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gcp_skus WHERE service = $1`, service); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &Ingest{ctx: ctx, tx: tx, batch: &pgx.Batch{}, vendor: VendorGCP, service: service}, nil
}

func (i *Ingest) Product(product AWSProduct) error {
	attributes, err := json.Marshal(product.Attributes)
	if err != nil {
		return err
	}
	i.batch.Queue(`INSERT INTO aws_products (service, region, sku, attributes) VALUES ($1, $2, $3, $4)
		ON CONFLICT (service, region, sku) DO UPDATE SET attributes = EXCLUDED.attributes`,
		i.service, i.region, product.SKU, string(attributes))
	return i.flush(batchSize)
}

func (i *Ingest) Price(price AWSPrice) error {
	i.batch.Queue(`INSERT INTO aws_prices (sku, unit, begin_range, end_range, price) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (sku, begin_range) DO UPDATE SET unit = EXCLUDED.unit, end_range = EXCLUDED.end_range, price = EXCLUDED.price`,
		price.SKU, price.Unit, price.BeginRange, price.EndRange, price.Price)
	return i.flush(batchSize)
}

func (i *Ingest) SKU(sku GCPSKU) error {
	category, err := json.Marshal(sku.Category)
	if err != nil {
		return err
	}
	tiers, err := json.Marshal(sku.Tiers)
	if err != nil {
		return err
	}
	i.batch.Queue(`INSERT INTO gcp_skus (sku_id, service, description, category, regions, tiers) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sku_id) DO UPDATE SET service = EXCLUDED.service, description = EXCLUDED.description,
		category = EXCLUDED.category, regions = EXCLUDED.regions, tiers = EXCLUDED.tiers`,
		sku.ID, sku.Service, sku.Description, string(category), sku.Regions, string(tiers))
	return i.flush(batchSize)
}

func (i *Ingest) flush(at int) error {
	if i.batch.Len() < at {
		return nil
	}
	if err := i.tx.SendBatch(i.ctx, i.batch).Close(); err != nil {
		return err
	}
	i.batch = &pgx.Batch{}
	return nil
}

func (i *Ingest) Commit(version string, fetched time.Time) error {
	if err := i.flush(1); err != nil {
		return err
	}
	if _, err := i.tx.Exec(i.ctx, `INSERT INTO ingests (vendor, service, region, version, fetched_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (vendor, service, region) DO UPDATE SET version = EXCLUDED.version, fetched_at = EXCLUDED.fetched_at`,
		i.vendor, i.service, i.region, version, fetched); err != nil {
		return err
	}
	return i.tx.Commit(i.ctx)
}

func (i *Ingest) Close() { _ = i.tx.Rollback(i.ctx) }
