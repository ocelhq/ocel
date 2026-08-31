package fake

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Records struct {
	journal *Journal

	mu       sync.Mutex
	seq      uint64
	rows     map[string]providerkit.Record
	refusals map[string]error
}

func NewRecords() *Records {
	return &Records{rows: map[string]providerkit.Record{}, refusals: map[string]error{}}
}

func (r *Records) RefuseRemoval(name providerkit.RecordName, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusals[name.String()] = err
}

func (r *Records) Read(_ context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[name.String()]
	if !ok {
		return providerkit.Record{}, providerkit.ErrNoRecord
	}
	return copyRecord(row), nil
}

func (r *Records) Write(_ context.Context, record providerkit.Record) (providerkit.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.held(record); err != nil {
		return "", err
	}
	return r.store(record), nil
}

func (r *Records) WritePair(_ context.Context, first, second providerkit.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range []providerkit.Record{first, second} {
		if err := r.held(record); err != nil {
			return err
		}
	}
	r.store(first)
	r.store(second)
	return nil
}

func (r *Records) held(record providerkit.Record) error {
	prior, exists := r.rows[record.Name.String()]
	if exists != (record.Revision != "") || (exists && prior.Revision != record.Revision) {
		return providerkit.ErrStale
	}
	return nil
}

func (r *Records) store(record providerkit.Record) providerkit.Revision {
	r.seq++
	next := copyRecord(record)
	next.Revision = providerkit.Revision(strconv.FormatUint(r.seq, 10))
	r.rows[record.Name.String()] = next
	return next.Revision
}

func (r *Records) Remove(_ context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := name.String()
	r.journal.note("forget " + key)
	if refused := r.refusals[key]; refused != nil {
		return refused
	}
	row, ok := r.rows[key]
	if !ok {
		return providerkit.ErrNoRecord
	}
	if row.Revision != expected {
		return providerkit.ErrStale
	}
	delete(r.rows, key)
	return nil
}

func (r *Records) List(_ context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := under.String()
	found := make([]providerkit.Record, 0, len(r.rows))
	for key := range maps.Keys(r.rows) {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			found = append(found, copyRecord(r.rows[key]))
		}
	}
	slices.SortFunc(found, func(a, b providerkit.Record) int {
		return strings.Compare(a.Name.String(), b.Name.String())
	})
	return found, nil
}

func (r *Records) Snapshot() map[string]providerkit.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	held := make(map[string]providerkit.Revision, len(r.rows))
	for key, row := range r.rows {
		held[key] = row.Revision
	}
	return held
}

func copyRecord(row providerkit.Record) providerkit.Record {
	return providerkit.Record{
		Name:     slices.Clone(row.Name),
		Bytes:    slices.Clone(row.Bytes),
		Revision: row.Revision,
	}
}
