package fake

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
)

const liveCapacity = 1024

type Logs struct {
	mu      sync.Mutex
	entries map[string][]provider.LogEntry
	deleted map[string]bool
	live    chan liveItem
}

type liveItem struct {
	physical string
	entries  []provider.LogEntry
	notice   *provider.LogNotice
}

func (l *Logs) liveItems() chan liveItem {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live == nil {
		l.live = make(chan liveItem, liveCapacity)
	}
	return l.live
}

func (l *Logs) Feed(physical string, entries ...provider.LogEntry) {
	l.liveItems() <- liveItem{physical: physical, entries: entries}
}

func (l *Logs) FeedNotice(notice provider.LogNotice) {
	l.liveItems() <- liveItem{notice: &notice}
}

func (l *Logs) Append(physical string, entries ...provider.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string][]provider.LogEntry{}
	}
	l.entries[physical] = append(l.entries[physical], entries...)
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
	if q.Tail && len(q.Targets) == 0 {
		return nil
	}
	if q.Tail {
		return l.follow(ctx, q, emit, notice)
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

func (l *Logs) follow(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	items := l.liveItems()
	for {
		select {
		case <-ctx.Done():
			return nil
		case item := <-items:
			if item.notice != nil {
				if err := notice(*item.notice); err != nil {
					return err
				}
				continue
			}
			var live []provider.LogEntry
			for _, target := range q.Targets {
				if target.Physical() != item.physical {
					continue
				}
				for _, entry := range item.entries {
					if !strings.Contains(entry.Message, q.Contains) {
						continue
					}
					entry.App, entry.Source, entry.Release = target.App, target.Source, target.Release
					live = append(live, entry)
				}
			}
			if len(live) == 0 {
				continue
			}
			if err := emit(live); err != nil {
				return err
			}
		}
	}
}
