package fake

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/records"
)

type KeyValues struct {
	journal *Journal

	mu       sync.Mutex
	seq      uint64
	rows     map[string]records.Entry
	refusals map[string]error
	moving   string
	hooks    map[string]func()
	under    *prefixedWrite
}

type prefixedWrite struct {
	partition records.Partition
	prefix    string
	run       func() error
}

func (s *KeyValues) BeforeNextWriteUnder(partition records.Partition, prefix string, run func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.under = &prefixedWrite{partition: partition, prefix: prefix, run: run}
}

func (s *KeyValues) runPrefixedHook(key records.Key) error {
	s.mu.Lock()
	under := s.under
	if under == nil || key.Partition.String() != under.partition.String() || !strings.HasPrefix(strings.Join(key.Path, "/"), under.prefix) {
		s.mu.Unlock()
		return nil
	}
	s.under = nil
	s.mu.Unlock()
	return under.run()
}

func NewKeyValues() *KeyValues {
	return &KeyValues{rows: map[string]records.Entry{}, refusals: map[string]error{}, hooks: map[string]func(){}}
}

func encodeKey(key records.Key) string {
	encoded, _ := json.Marshal([]any{key.Partition.Tier, key.Partition.Root, key.Partition.Path, key.Path})
	return string(encoded)
}

func (s *KeyValues) SetRemovalError(key records.Key, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusals[encodeKey(key)] = err
}

func (s *KeyValues) MoveBeforeNextWrite(key records.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moving = encodeKey(key)
}

func (s *KeyValues) BeforeNextWrite(key records.Key, run func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[encodeKey(key)] = run
}

func (s *KeyValues) runWriteHook(key records.Key) {
	s.mu.Lock()
	run, ok := s.hooks[encodeKey(key)]
	delete(s.hooks, encodeKey(key))
	s.mu.Unlock()
	if ok {
		run()
	}
}

func (s *KeyValues) Read(_ context.Context, key records.Key) (records.Entry, error) {
	if err := records.RefuseMalformedKey(key); err != nil {
		return records.Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[encodeKey(key)]
	if !ok {
		return records.Entry{}, records.ErrNotFound
	}
	return copyEntry(row), nil
}

func (s *KeyValues) Write(_ context.Context, entry records.Entry) (records.Revision, error) {
	if err := records.RefuseUnwritable(entry); err != nil {
		return "", err
	}
	s.runWriteHook(entry.Key)
	if err := s.runPrefixedHook(entry.Key); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := encodeKey(entry.Key); row == s.moving {
		s.moving = ""
		if prior, exists := s.rows[row]; exists {
			s.store(prior)
		}
	}
	if err := s.refuseMovedRevision(entry); err != nil {
		return "", err
	}
	return s.store(entry), nil
}

func (s *KeyValues) WritePair(_ context.Context, first, second records.Entry) error {
	for _, entry := range []records.Entry{first, second} {
		if err := records.RefuseUnwritable(entry); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range []records.Entry{first, second} {
		if err := s.refuseMovedRevision(entry); err != nil {
			return err
		}
	}
	s.store(first)
	s.store(second)
	return nil
}

func (s *KeyValues) refuseMovedRevision(entry records.Entry) error {
	prior, exists := s.rows[encodeKey(entry.Key)]
	if exists != (entry.Revision != "") || (exists && prior.Revision != entry.Revision) {
		return records.ErrStale
	}
	return nil
}

func (s *KeyValues) store(entry records.Entry) records.Revision {
	s.seq++
	next := copyEntry(entry)
	next.Revision = records.Revision(strconv.FormatUint(s.seq, 10))
	s.rows[encodeKey(entry.Key)] = next
	return next.Revision
}

func (s *KeyValues) Remove(_ context.Context, key records.Key, expected records.Revision) error {
	if err := records.RefuseMalformedKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := encodeKey(key)
	s.journal.note("forget " + key.String())
	if refused := s.refusals[row]; refused != nil {
		return refused
	}
	recorded, ok := s.rows[row]
	if !ok {
		return records.ErrNotFound
	}
	if recorded.Revision != expected {
		return records.ErrStale
	}
	delete(s.rows, row)
	return nil
}

func (s *KeyValues) List(_ context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	if err := records.RefuseMalformedPartition(in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []records.Entry
	for _, entry := range s.rows {
		if !isSamePartition(entry.Key.Partition, in) || len(entry.Key.Path) < len(under) || !slices.Equal(entry.Key.Path[:len(under)], under) {
			continue
		}
		found = append(found, copyEntry(entry))
	}
	slices.SortFunc(found, func(a, b records.Entry) int { return slices.Compare(a.Key.Path, b.Key.Path) })
	return found, nil
}

func isSamePartition(a, b records.Partition) bool {
	return a.Tier == b.Tier && a.Root == b.Root && slices.Equal(a.Path, b.Path)
}

func (s *KeyValues) Snapshot() map[string]records.Revision {
	s.mu.Lock()
	defer s.mu.Unlock()
	revisions := make(map[string]records.Revision, len(s.rows))
	for row, entry := range s.rows {
		revisions[row] = entry.Revision
	}
	return revisions
}

func copyEntry(entry records.Entry) records.Entry {
	return records.Entry{
		Key: records.Key{
			Partition: records.Partition{Tier: entry.Key.Partition.Tier, Root: entry.Key.Partition.Root, Path: slices.Clone(entry.Key.Partition.Path)},
			Path:      slices.Clone(entry.Key.Path),
		},
		Value:    slices.Clone(entry.Value),
		Revision: entry.Revision,
	}
}
