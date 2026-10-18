package fake

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

type runKey struct{ topic, execution string }

type recordKey struct {
	purpose    provider.RecordPurpose
	topic, key string
}

type Store struct {
	mu      sync.Mutex
	runs    map[runKey]runs.Run
	records map[recordKey]provider.ExpiringRecord
}

func NewStore() *Store {
	return &Store{runs: map[runKey]runs.Run{}, records: map[recordKey]provider.ExpiringRecord{}}
}

func (s *Store) CreateRun(_ context.Context, run runs.Run) (keyvalue.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runKey{run.Topic, run.Execution}
	if _, found := s.runs[key]; found {
		return "", keyvalue.ErrStale
	}
	return s.put(key, run)
}

func (s *Store) put(key runKey, run runs.Run) (keyvalue.Revision, error) {
	revision, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	run.Revision = revision
	run.Tags = slices.Clone(run.Tags)
	s.runs[key] = run
	return revision, nil
}

func (s *Store) ReadRun(_ context.Context, topic, execution string) (runs.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, found := s.runs[runKey{topic, execution}]
	if !found {
		return runs.Run{}, keyvalue.ErrNotFound
	}
	run.Tags = slices.Clone(run.Tags)
	return run, nil
}

func (s *Store) WriteRun(_ context.Context, run runs.Run) (keyvalue.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runKey{run.Topic, run.Execution}
	stored, found := s.runs[key]
	if !found || run.Revision == "" || stored.Revision != run.Revision {
		return "", keyvalue.ErrStale
	}
	return s.put(key, run)
}

func (s *Store) DeleteRun(_ context.Context, run runs.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runKey{run.Topic, run.Execution}
	stored, found := s.runs[key]
	if !found || stored.Revision != run.Revision {
		return keyvalue.ErrStale
	}
	delete(s.runs, key)
	return nil
}

func (s *Store) matching(filter runs.RunFilter) []runs.Run {
	var matched []runs.Run
	for _, run := range s.runs {
		if !slices.Contains(filter.Topics, run.Topic) || (filter.Consumer != "" && run.Consumer != filter.Consumer) {
			continue
		}
		if len(filter.Statuses) > 0 && !slices.Contains(filter.Statuses, run.Status) {
			continue
		}
		if !holdsEvery(run.Tags, filter.Tags) {
			continue
		}
		matched = append(matched, run)
	}
	slices.SortFunc(matched, newestFirst)
	return matched
}

func newestFirst(a, b runs.Run) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}
	return cmp.Compare(b.Execution, a.Execution)
}

func holdsEvery(tags, wanted []string) bool {
	for _, tag := range wanted {
		if !slices.Contains(tags, tag) {
			return false
		}
	}
	return true
}

func (s *Store) ListRuns(_ context.Context, filter runs.RunFilter) (runs.RunPage, error) {
	limit := runs.PageLimit(filter.Limit)
	var after runs.Run
	if filter.Cursor != "" {
		created, execution, err := runs.ParseCursor(filter.Cursor)
		if err != nil {
			return runs.RunPage{}, err
		}
		after = runs.Run{CreatedAt: created, Execution: execution}
	}
	s.mu.Lock()
	matched := s.matching(filter)
	s.mu.Unlock()
	var page runs.RunPage
	for _, run := range matched {
		if filter.Cursor != "" && newestFirst(after, run) >= 0 {
			continue
		}
		if len(page.Runs) == limit {
			last := page.Runs[limit-1]
			page.NextCursor = runs.CursorOf(last.CreatedAt, last.Execution)
			break
		}
		page.Runs = append(page.Runs, run)
	}
	return page, nil
}

func (s *Store) CountRuns(_ context.Context, filter runs.RunFilter) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.matching(filter))), nil
}

func (s *Store) EnsureRecord(_ context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey{record.Purpose, record.Topic, record.Key}
	if held, found := s.records[key]; found && held.ExpiresAt.After(time.Now()) {
		return held, false, nil
	}
	s.records[key] = record
	return record, true, nil
}

func (s *Store) ReplaceRecord(_ context.Context, record provider.ExpiringRecord, held json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey{record.Purpose, record.Topic, record.Key}
	if current, found := s.records[key]; !found || !bytes.Equal(current.Value, held) {
		return keyvalue.ErrStale
	}
	s.records[key] = record
	return nil
}

func (s *Store) DeleteRecord(_ context.Context, record provider.ExpiringRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey{record.Purpose, record.Topic, record.Key}
	if held, found := s.records[key]; found && bytes.Equal(held.Value, record.Value) {
		delete(s.records, key)
	}
	return nil
}
