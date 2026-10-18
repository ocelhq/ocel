package gcp

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type keyValues struct{ p *Provider }

func (s keyValues) open(ctx context.Context) (*clients, ports.KeyValues, error) {
	resolved, err := s.p.openClients(ctx)
	if err != nil {
		return nil, ports.KeyValues{}, err
	}
	return resolved, ports.KeyValues{Clients: resolved.Workload()}, nil
}

func (s keyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	_, store, err := s.open(ctx)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	return store.Read(ctx, key)
}

func (s keyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	c, store, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	revision, err := store.Write(ctx, entry)
	if !errors.Is(err, ports.ErrNoProjectRecords) {
		return revision, err
	}
	if err := ensureProjectDatabaseOf(ctx, c, entry.Key.Partition); err != nil {
		return "", err
	}
	return store.Write(ctx, entry)
}

func (s keyValues) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	c, store, err := s.open(ctx)
	if err != nil {
		return err
	}
	err = store.WritePair(ctx, first, second)
	if !errors.Is(err, ports.ErrNoProjectRecords) {
		return err
	}
	if err := ensureProjectDatabaseOf(ctx, c, first.Key.Partition); err != nil {
		return err
	}
	return store.WritePair(ctx, first, second)
}

func ensureProjectDatabaseOf(ctx context.Context, c *clients, in keyvalue.Partition) error {
	project, _ := ports.ProjectOfRecords(in)
	return c.ensureProjectDatabase(ctx, in.Tier, project)
}

func (s keyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	_, store, err := s.open(ctx)
	if err != nil {
		return err
	}
	return store.Remove(ctx, key, expected)
}

func (s keyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	_, store, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, in, under...)
}
