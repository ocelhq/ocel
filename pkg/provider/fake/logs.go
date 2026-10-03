package fake

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
)

type Logs struct {
	mu      sync.Mutex
	entries map[string][]provider.LogEntry
}

func (l *Logs) Append(physical string, entries ...provider.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string][]provider.LogEntry{}
	}
	l.entries[physical] = append(l.entries[physical], entries...)
}

func (l *Logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	if q.Limit < 1 {
		return nil
	}
	var read []provider.LogEntry
	for _, target := range q.Targets {
		for _, entry := range l.entriesOf(target) {
			if entry.Time.Before(q.Since) || !q.Until.IsZero() && entry.Time.After(q.Until) || !strings.Contains(entry.Message, q.Contains) {
				continue
			}
			entry.App, entry.Source, entry.Release = target.App, target.Source, target.Release
			read = append(read, entry)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slices.SortStableFunc(read, func(a, b provider.LogEntry) int { return a.Time.Compare(b.Time) })
	read = read[len(read)-min(len(read), q.Limit):]
	if len(read) == 0 {
		return nil
	}
	return emit(read)
}

func (l *Logs) entriesOf(target provider.LogTarget) []provider.LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries[target.Physical()])
}
