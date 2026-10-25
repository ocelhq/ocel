package gcp

import (
	"context"

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
	_, store, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	if err := s.p.ensureRecordedDatabase(ctx, entry.Key.Partition); err != nil {
		return "", err
	}
	return store.Write(ctx, entry)
}

func (s keyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	_, store, err := s.open(ctx)
	if err != nil {
		return err
	}
	for _, entry := range []records.Entry{first, second} {
		if err := s.p.ensureRecordedDatabase(ctx, entry.Key.Partition); err != nil {
			return err
		}
	}
	return store.WritePair(ctx, first, second)
}

func (p *Provider) ensureRecordedDatabase(ctx context.Context, in records.Partition) error {
	project, owned := ports.ProjectOfRecords(in)
	if !owned {
		return nil
	}
	recorded := string(in.Tier) + "/" + project
	if _, done := p.recordedDatabases.Load(recorded); done {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	if err := variables.EnsureProjectDatabaseRecorded(ctx, b, in.Tier, project, p.options.OwnDatabase, nil, appDatabaseLayouts()...); err != nil {
		return err
	}
	p.recordedDatabases.Store(recorded, true)
	return nil
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
