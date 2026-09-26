package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type recordStore struct{ p *Provider }

func (r recordStore) stood(ctx context.Context) (ports.Records, error) {
	held, err := r.p.stood(ctx)
	if err != nil {
		return ports.Records{}, err
	}
	return ports.Records{Clients: held.Workload()}, nil
}

func (r recordStore) Read(ctx context.Context, name records.Name) (records.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return records.Record{}, err
	}
	return held.Read(ctx, name)
}

func (r recordStore) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return "", err
	}
	return held.Write(ctx, record)
}

func (r recordStore) WritePair(ctx context.Context, first, second records.Record) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.WritePair(ctx, first, second)
}

func (r recordStore) Remove(ctx context.Context, name records.Name, expected records.Revision) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.Remove(ctx, name, expected)
}

func (r recordStore) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.List(ctx, under)
}
