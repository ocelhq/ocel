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
	mu   sync.Mutex
	seq  uint64
	rows map[string]providerkit.Record
}

func NewRecords() *Records {
	return &Records{rows: map[string]providerkit.Record{}}
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
	key := record.Name.String()
	prior, exists := r.rows[key]
	switch {
	case !exists && record.Revision != "":
		return "", providerkit.ErrStale
	case exists && prior.Revision != record.Revision:
		return "", providerkit.ErrStale
	}
	r.seq++
	next := copyRecord(record)
	next.Revision = providerkit.Revision(strconv.FormatUint(r.seq, 10))
	r.rows[key] = next
	return next.Revision, nil
}

func (r *Records) Remove(_ context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := name.String()
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

func copyRecord(row providerkit.Record) providerkit.Record {
	return providerkit.Record{
		Name:     slices.Clone(row.Name),
		Bytes:    slices.Clone(row.Bytes),
		Revision: row.Revision,
	}
}
