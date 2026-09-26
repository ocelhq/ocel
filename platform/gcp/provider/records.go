package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type recordStore struct{ p *Provider }

func (r recordStore) openRecords(ctx context.Context) (ports.Records, error) {
	resolved, err := r.p.openClients(ctx)
	if err != nil {
		return ports.Records{}, err
	}
	return ports.Records{Clients: resolved.Workload()}, nil
}

func (r recordStore) Read(ctx context.Context, name records.Name) (records.Record, error) {
	store, err := r.openRecords(ctx)
	if err != nil {
		return records.Record{}, err
	}
	return store.Read(ctx, name)
}

func (r recordStore) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	store, err := r.openRecords(ctx)
	if err != nil {
		return "", err
	}
	return store.Write(ctx, record)
}

func (r recordStore) WritePair(ctx context.Context, first, second records.Record) error {
	store, err := r.openRecords(ctx)
	if err != nil {
		return err
	}
	return store.WritePair(ctx, first, second)
}

func (r recordStore) Remove(ctx context.Context, name records.Name, expected records.Revision) error {
	store, err := r.openRecords(ctx)
	if err != nil {
		return err
	}
	return store.Remove(ctx, name, expected)
}

func (r recordStore) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	store, err := r.openRecords(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, under)
}
