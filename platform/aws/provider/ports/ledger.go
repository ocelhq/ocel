package ports

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func Ledger(dynamo DynamoAPI, table string, class edge.Class, slug string) *ledger.Ledger {
	return ledger.New(ledgerRecords{Records{Dynamo: dynamo, Table: table}}, class, slug)
}

type ledgerRecords struct{ Records }

func (r ledgerRecords) provisioned() error {
	if r.Dynamo == nil {
		return fmt.Errorf("%w: the deployments ledger has no DynamoDB client; bootstrap the account first", edge.ErrStoreAbsent)
	}
	if r.Table == "" {
		return fmt.Errorf("%w: the deployments ledger names no state table; bootstrap the account first", edge.ErrStoreAbsent)
	}
	return nil
}

func (r ledgerRecords) Read(ctx context.Context, name kit.RecordName) (kit.Record, error) {
	if err := r.provisioned(); err != nil {
		return kit.Record{}, err
	}
	return r.Records.Read(ctx, name)
}

func (r ledgerRecords) Write(ctx context.Context, record kit.Record) (kit.Revision, error) {
	if err := r.provisioned(); err != nil {
		return "", err
	}
	return r.Records.Write(ctx, record)
}

func (r ledgerRecords) Remove(ctx context.Context, name kit.RecordName, expected kit.Revision) error {
	if err := r.provisioned(); err != nil {
		return err
	}
	return r.Records.Remove(ctx, name, expected)
}

func (r ledgerRecords) List(ctx context.Context, under kit.RecordName) ([]kit.Record, error) {
	if err := r.provisioned(); err != nil {
		return nil, err
	}
	return r.Records.List(ctx, under)
}
