package fake

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
)

type Logs struct {
	mu      sync.Mutex
	entries map[string][]provider.LogEntry
	deleted map[string]bool
	written int
	tails   map[*logTail]bool
}

type logTail struct {
	query      provider.LogQuery
	mu         sync.Mutex
	deliveries []delivery
	arrived    chan struct{}
}

type delivery struct {
	entries []provider.LogEntry
	notice  *provider.LogNotice
}

func (l *Logs) Append(physical string, entries ...provider.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string][]provider.LogEntry{}
	}
	for _, entry := range entries {
		if entry.ID == "" {
			l.written++
			entry.ID = "fake-" + strconv.Itoa(l.written)
		}
		l.entries[physical] = append(l.entries[physical], entry)
		for tail := range l.tails {
			if matched := tail.matching(physical, []provider.LogEntry{entry}); len(matched) > 0 {
				tail.deliver(delivery{entries: matched})
			}
		}
	}
}

func (l *Logs) SendNotice(notice provider.LogNotice) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for tail := range l.tails {
		tail.deliver(delivery{notice: &notice})
	}
}

func (l *Logs) Delete(physical string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deleted == nil {
		l.deleted = map[string]bool{}
	}
	l.deleted[physical] = true
}

func (l *Logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	if q.Tail {
		if len(q.Targets) == 0 {
			return nil
		}
		return l.tail(ctx, q, emit, notice)
	}
	if q.Limit < 1 {
		return nil
	}
	var read []provider.LogEntry
	var missing []provider.LogTarget
	for _, target := range q.Targets {
		entries, deleted := l.entriesOf(target)
		if deleted {
			missing = append(missing, target)
			continue
		}
		for _, entry := range entries {
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
	if len(read) > 0 {
		if err := emit(read); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return provider.LogTargetsMissing{Targets: missing}
	}
	return nil
}

func (l *Logs) entriesOf(target provider.LogTarget) ([]provider.LogEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries[target.Physical()]), l.deleted[target.Physical()]
}

func (l *Logs) tail(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	tail := l.openTail(q)
	defer l.closeTail(tail)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tail.arrived:
		}
		for _, next := range tail.takeDeliveries() {
			var err error
			if next.notice != nil {
				err = notice(*next.notice)
			} else {
				err = emit(next.entries)
			}
			if err != nil {
				return err
			}
		}
	}
}

func (l *Logs) openTail(q provider.LogQuery) *logTail {
	l.mu.Lock()
	defer l.mu.Unlock()
	tail := &logTail{query: q, arrived: make(chan struct{}, 1)}
	var stored []provider.LogEntry
	for _, target := range q.Targets {
		stored = append(stored, tail.matching(target.Physical(), l.entries[target.Physical()])...)
	}
	slices.SortStableFunc(stored, func(a, b provider.LogEntry) int { return a.Time.Compare(b.Time) })
	if len(stored) > 0 {
		tail.deliver(delivery{entries: stored})
	}
	if l.tails == nil {
		l.tails = map[*logTail]bool{}
	}
	l.tails[tail] = true
	return tail
}

func (l *Logs) closeTail(tail *logTail) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tails, tail)
}

func (t *logTail) matching(physical string, entries []provider.LogEntry) []provider.LogEntry {
	var matched []provider.LogEntry
	for _, target := range t.query.Targets {
		if target.Physical() != physical {
			continue
		}
		for _, entry := range entries {
			if entry.Time.Before(t.query.Since) || !strings.Contains(entry.Message, t.query.Contains) {
				continue
			}
			entry.App, entry.Source, entry.Release = target.App, target.Source, target.Release
			matched = append(matched, entry)
		}
	}
	return matched
}

func (t *logTail) deliver(next delivery) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deliveries = append(t.deliveries, next)
	select {
	case t.arrived <- struct{}{}:
	default:
	}
}

func (t *logTail) takeDeliveries() []delivery {
	t.mu.Lock()
	defer t.mu.Unlock()
	taken := t.deliveries
	t.deliveries = nil
	return taken
}
