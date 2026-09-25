package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type records struct{ p *Provider }

func (r records) stood(ctx context.Context) (ports.Records, error) {
	held, err := r.p.stood(ctx)
	if err != nil {
		return ports.Records{}, err
	}
	return ports.Records{Clients: held.Workload()}, nil
}

func (r records) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return providerkit.Record{}, err
	}
	return held.Read(ctx, name)
}

func (r records) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return "", err
	}
	return held.Write(ctx, record)
}

func (r records) WritePair(ctx context.Context, first, second providerkit.Record) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.WritePair(ctx, first, second)
}

func (r records) Remove(ctx context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.Remove(ctx, name, expected)
}

func (r records) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.List(ctx, under)
}
