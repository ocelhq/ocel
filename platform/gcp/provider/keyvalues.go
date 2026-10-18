package gcp

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

type keyValues struct{ p *Provider }

func (s keyValues) open(ctx context.Context) (*gcloud.Clients, ports.KeyValues, error) {
	resolved, err := s.p.openClients(ctx)
	if err != nil {
		return nil, ports.KeyValues{}, err
	}
	return resolved, ports.KeyValues{Clients: resolved.Workload()}, nil
}

func (s keyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	_, store, err := s.open(ctx)
	if err != nil {
		return records.Entry{}, err
	}
	return store.Read(ctx, key)
}

func (s keyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
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

func (s keyValues) WritePair(ctx context.Context, first, second records.Entry) error {
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

func ensureProjectDatabaseOf(ctx context.Context, c *gcloud.Clients, in records.Partition) error {
	project, _ := ports.ProjectOfRecords(in)
	return variables.EnsureProjectDatabase(ctx, c, in.Tier, project)
}

func (s keyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	_, store, err := s.open(ctx)
	if err != nil {
		return err
	}
	return store.Remove(ctx, key, expected)
}

func (s keyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	_, store, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, in, under...)
}
