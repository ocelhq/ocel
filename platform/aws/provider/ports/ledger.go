package ports

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
)

func Ledger(dynamo DynamoAPI, tables Tables, tier environment.Tier, slug string) *ledger.Ledger {
	return ledger.New(ledgerKeyValues{KeyValues{Dynamo: dynamo, Tables: tables}}, tier, slug)
}

type ledgerKeyValues struct{ KeyValues }

func (s ledgerKeyValues) refuseUnprovisioned(ctx context.Context, in records.Partition) error {
	if s.Dynamo == nil {
		return fmt.Errorf("%w: the deployments ledger has no DynamoDB client; bootstrap the account first", edge.ErrStoreAbsent)
	}
	table, err := s.table(ctx, in)
	if err != nil {
		return err
	}
	if table == "" {
		return fmt.Errorf("%w: the deployments ledger names no state table; bootstrap the account first", edge.ErrStoreAbsent)
	}
	return nil
}

func (s ledgerKeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if err := s.refuseUnprovisioned(ctx, key.Partition); err != nil {
		return records.Entry{}, err
	}
	return s.KeyValues.Read(ctx, key)
}

func (s ledgerKeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	if err := s.refuseUnprovisioned(ctx, entry.Key.Partition); err != nil {
		return "", err
	}
	return s.KeyValues.Write(ctx, entry)
}

func (s ledgerKeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	if err := s.refuseUnprovisioned(ctx, key.Partition); err != nil {
		return err
	}
	return s.KeyValues.Remove(ctx, key, expected)
}

func (s ledgerKeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	if err := s.refuseUnprovisioned(ctx, in); err != nil {
		return nil, err
	}
	return s.KeyValues.List(ctx, in, under...)
}
