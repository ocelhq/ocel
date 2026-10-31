package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type KeyValues struct{ Provider *Provider }

func (s KeyValues) open(ctx context.Context) (records.Store, error) {
	if s.Provider.Records != nil {
		return s.Provider.Records, nil
	}
	resolved, err := s.Provider.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	return ports.KeyValues{Clients: resolved.Workload()}, nil
}

func (s KeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	store, err := s.open(ctx)
	if err != nil {
		return records.Entry{}, err
	}
	return store.Read(ctx, key)
}

func (s KeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	store, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	if err := s.Provider.ensureRecordedDatabase(ctx, entry.Key.Partition); err != nil {
		return "", err
	}
	return store.Write(ctx, entry)
}

func (s KeyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	store, err := s.open(ctx)
	if err != nil {
		return err
	}
	for _, entry := range []records.Entry{first, second} {
		if err := s.Provider.ensureRecordedDatabase(ctx, entry.Key.Partition); err != nil {
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
	if err := p.ensureProjectDatabaseRecorded(ctx, in.Tier, project); err != nil {
		return err
	}
	p.recordedDatabases.Store(recorded, true)
	return nil
}

func (p *Provider) ensureProjectDatabaseRecorded(ctx context.Context, tier environment.Tier, project string) error {
	_, found, err := ports.ReadProjectDatabaseRecord(ctx, p.KeyValues(), tier, project)
	if err != nil || found {
		return err
	}
	return p.Engine.EnsureProjectDatabaseRecorded(ctx, tier, project)
}

func (s KeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	store, err := s.open(ctx)
	if err != nil {
		return err
	}
	return store.Remove(ctx, key, expected)
}

func (s KeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	store, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, in, under...)
}
