package fake

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"sync"

	"github.com/ocelhq/ocel/pkg/keyvalue"
)

type KeyValues struct {
	journal *Journal

	mu       sync.Mutex
	seq      uint64
	rows     map[string]keyvalue.Entry
	refusals map[string]error
}

func NewKeyValues() *KeyValues {
	return &KeyValues{rows: map[string]keyvalue.Entry{}, refusals: map[string]error{}}
}

func rowOf(key keyvalue.Key) string {
	encoded, _ := json.Marshal([]any{key.Partition.Tier, key.Partition.Root, key.Partition.Path, key.Path})
	return string(encoded)
}

func (s *KeyValues) RefuseRemoval(key keyvalue.Key, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusals[rowOf(key)] = err
}

func (s *KeyValues) Read(_ context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return keyvalue.Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[rowOf(key)]
	if !ok {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return copyEntry(row), nil
}

func (s *KeyValues) Write(_ context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := refuseUnwritable(entry); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkRevision(entry); err != nil {
		return "", err
	}
	return s.store(entry), nil
}

func (s *KeyValues) WritePair(_ context.Context, first, second keyvalue.Entry) error {
	for _, entry := range []keyvalue.Entry{first, second} {
		if err := refuseUnwritable(entry); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range []keyvalue.Entry{first, second} {
		if err := s.checkRevision(entry); err != nil {
			return err
		}
	}
	s.store(first)
	s.store(second)
	return nil
}

func refuseUnwritable(entry keyvalue.Entry) error {
	if err := keyvalue.RefuseMalformedKey(entry.Key); err != nil {
		return err
	}
	return keyvalue.RefuseNonJSON(entry)
}

func (s *KeyValues) checkRevision(entry keyvalue.Entry) error {
	prior, exists := s.rows[rowOf(entry.Key)]
	if exists != (entry.Revision != "") || (exists && prior.Revision != entry.Revision) {
		return keyvalue.ErrStale
	}
	return nil
}

func (s *KeyValues) store(entry keyvalue.Entry) keyvalue.Revision {
	s.seq++
	next := copyEntry(entry)
	next.Revision = keyvalue.Revision(strconv.FormatUint(s.seq, 10))
	s.rows[rowOf(entry.Key)] = next
	return next.Revision
}

func (s *KeyValues) Remove(_ context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := rowOf(key)
	s.journal.note("forget " + key.String())
	if refused := s.refusals[row]; refused != nil {
		return refused
	}
	recorded, ok := s.rows[row]
	if !ok {
		return keyvalue.ErrNotFound
	}
	if recorded.Revision != expected {
		return keyvalue.ErrStale
	}
	delete(s.rows, row)
	return nil
}

func (s *KeyValues) List(_ context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if err := keyvalue.RefuseMalformedPartition(in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []keyvalue.Entry
	for _, entry := range s.rows {
		if !samePartition(entry.Key.Partition, in) || len(entry.Key.Path) < len(under) || !slices.Equal(entry.Key.Path[:len(under)], under) {
			continue
		}
		found = append(found, copyEntry(entry))
	}
	slices.SortFunc(found, func(a, b keyvalue.Entry) int { return slices.Compare(a.Key.Path, b.Key.Path) })
	return found, nil
}

func samePartition(a, b keyvalue.Partition) bool {
	return a.Tier == b.Tier && a.Root == b.Root && slices.Equal(a.Path, b.Path)
}

func (s *KeyValues) Snapshot() map[string]keyvalue.Revision {
	s.mu.Lock()
	defer s.mu.Unlock()
	revisions := make(map[string]keyvalue.Revision, len(s.rows))
	for row, entry := range s.rows {
		revisions[row] = entry.Revision
	}
	return revisions
}

func copyEntry(entry keyvalue.Entry) keyvalue.Entry {
	return keyvalue.Entry{
		Key: keyvalue.Key{
			Partition: keyvalue.Partition{Tier: entry.Key.Partition.Tier, Root: entry.Key.Partition.Root, Path: slices.Clone(entry.Key.Partition.Path)},
			Path:      slices.Clone(entry.Key.Path),
		},
		Value:    slices.Clone(entry.Value),
		Revision: entry.Revision,
	}
}
