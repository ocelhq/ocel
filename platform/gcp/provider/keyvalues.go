package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type keyValues struct{ p *Provider }

func (s keyValues) open(ctx context.Context) (ports.KeyValues, error) {
	resolved, err := s.p.openClients(ctx)
	if err != nil {
		return ports.KeyValues{}, err
	}
	return ports.KeyValues{Clients: resolved.Workload()}, nil
}

func (s keyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	store, err := s.open(ctx)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	return store.Read(ctx, key)
}

func (s keyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	store, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	return store.Write(ctx, entry)
}

func (s keyValues) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	store, err := s.open(ctx)
	if err != nil {
		return err
	}
	return store.WritePair(ctx, first, second)
}

func (s keyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	store, err := s.open(ctx)
	if err != nil {
		return err
	}
	return store.Remove(ctx, key, expected)
}

func (s keyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	store, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, in, under...)
}
