package ports

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func Ledger(dynamo DynamoAPI, tables Tables, class edge.Class, slug string) *ledger.Ledger {
	return ledger.New(ledgerRecords{Records{Dynamo: dynamo, Tables: tables}}, class, slug)
}

type ledgerRecords struct{ Records }

func (r ledgerRecords) provisioned(ctx context.Context, name records.Name) error {
	if r.Dynamo == nil {
		return fmt.Errorf("%w: the deployments ledger has no DynamoDB client; bootstrap the account first", edge.ErrStoreAbsent)
	}
	table, err := r.table(ctx, name)
	if err != nil {
		return err
	}
	if table == "" {
		return fmt.Errorf("%w: the deployments ledger names no state table; bootstrap the account first", edge.ErrStoreAbsent)
	}
	return nil
}

func (r ledgerRecords) Read(ctx context.Context, name records.Name) (records.Record, error) {
	if err := r.provisioned(ctx, name); err != nil {
		return records.Record{}, err
	}
	return r.Records.Read(ctx, name)
}

func (r ledgerRecords) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	if err := r.provisioned(ctx, record.Name); err != nil {
		return "", err
	}
	return r.Records.Write(ctx, record)
}

func (r ledgerRecords) Remove(ctx context.Context, name records.Name, expected records.Revision) error {
	if err := r.provisioned(ctx, name); err != nil {
		return err
	}
	return r.Records.Remove(ctx, name, expected)
}

func (r ledgerRecords) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	if err := r.provisioned(ctx, under); err != nil {
		return nil, err
	}
	return r.Records.List(ctx, under)
}
