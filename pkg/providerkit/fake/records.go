package fake

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
)

type Records struct {
	journal *Journal

	mu       sync.Mutex
	seq      uint64
	rows     map[string]records.Record
	refusals map[string]error
}

func NewRecords() *Records {
	return &Records{rows: map[string]records.Record{}, refusals: map[string]error{}}
}

func (r *Records) RefuseRemoval(name records.Name, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusals[name.String()] = err
}

func (r *Records) Read(_ context.Context, name records.Name) (records.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[name.String()]
	if !ok {
		return records.Record{}, records.ErrNotFound
	}
	return copyRecord(row), nil
}

func (r *Records) Write(_ context.Context, record records.Record) (records.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkRevision(record); err != nil {
		return "", err
	}
	return r.store(record), nil
}

func (r *Records) WritePair(_ context.Context, first, second records.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range []records.Record{first, second} {
		if err := r.checkRevision(record); err != nil {
			return err
		}
	}
	r.store(first)
	r.store(second)
	return nil
}

func (r *Records) checkRevision(record records.Record) error {
	prior, exists := r.rows[record.Name.String()]
	if exists != (record.Revision != "") || (exists && prior.Revision != record.Revision) {
		return records.ErrStale
	}
	return nil
}

func (r *Records) store(record records.Record) records.Revision {
	r.seq++
	next := copyRecord(record)
	next.Revision = records.Revision(strconv.FormatUint(r.seq, 10))
	r.rows[record.Name.String()] = next
	return next.Revision
}

func (r *Records) Remove(_ context.Context, name records.Name, expected records.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := name.String()
	r.journal.note("forget " + key)
	if refused := r.refusals[key]; refused != nil {
		return refused
	}
	row, ok := r.rows[key]
	if !ok {
		return records.ErrNotFound
	}
	if row.Revision != expected {
		return records.ErrStale
	}
	delete(r.rows, key)
	return nil
}

func (r *Records) List(_ context.Context, under records.Name) ([]records.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := under.String()
	found := make([]records.Record, 0, len(r.rows))
	for key := range maps.Keys(r.rows) {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			found = append(found, copyRecord(r.rows[key]))
		}
	}
	slices.SortFunc(found, func(a, b records.Record) int {
		return strings.Compare(a.Name.String(), b.Name.String())
	})
	return found, nil
}

func (r *Records) Snapshot() map[string]records.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	revisions := make(map[string]records.Revision, len(r.rows))
	for key, row := range r.rows {
		revisions[key] = row.Revision
	}
	return revisions
}

func copyRecord(row records.Record) records.Record {
	return records.Record{
		Name:     slices.Clone(row.Name),
		Bytes:    slices.Clone(row.Bytes),
		Revision: row.Revision,
	}
}
