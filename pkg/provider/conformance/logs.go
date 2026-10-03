package conformance

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type LogChecks struct {
	Seed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)
}

const cancelledReadDeadline = time.Second

var logsEpoch = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

func runLogs(t *testing.T, suite Suite) {
	t.Helper()

	construct := suite.New
	if construct == nil {
		construct = suite.Server.New
	}
	if construct == nil {
		t.Skip("this suite has no constructor, so there is no Logs port to exercise")
	}
	p, err := construct(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	if suite.New == nil {
		RunLogs(t, p.Facts(), p.Logs())
	}
	if suite.Logs != nil {
		RunLogHistory(t, p.Facts(), p.Logs(), suite.Logs.Seed)
	}
}

func logQuery(targets ...provider.LogTarget) provider.LogQuery {
	return provider.LogQuery{
		Tier:    environment.TierProduction,
		Slug:    "conformance",
		Env:     "production",
		Targets: targets,
		Since:   logsEpoch.Add(-time.Hour),
		Limit:   100,
	}
}

func targetOn(facts provider.Facts, physical string) provider.LogTarget {
	target := provider.LogTarget{App: "web", Release: "r1", Source: "http"}
	if slices.Contains(facts.Computes, provider.ComputeServerless) {
		target.Function = &provider.Function{Name: "web", Physical: physical, Revision: physical + "-r1"}
		return target
	}
	target.Container = &provider.AppContainer{Name: "web", Physical: physical, Revision: physical + "-r1"}
	return target
}

func RunLogs(t *testing.T, facts provider.Facts, logs provider.Logs) {
	t.Helper()

	t.Run("an empty target list reads nothing and fails nothing", func(t *testing.T) {
		emit := func(entries []provider.LogEntry) error {
			t.Errorf("Read() with no targets emitted %v, want nothing", entries)
			return nil
		}
		if err := logs.Read(context.Background(), logQuery(), emit); err != nil {
			t.Errorf("Read() with no targets error = %v, want none", err)
		}
	})

	t.Run("a read returns within a second of its context being cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = logs.Read(ctx, logQuery(targetOn(facts, "conformance-cancelled")), func([]provider.LogEntry) error { return nil })
		}()
		select {
		case <-done:
		case <-time.After(cancelledReadDeadline):
			t.Errorf("Read() was still running %s after its context was cancelled", cancelledReadDeadline)
		}
	})
}

func RunLogHistory(t *testing.T, facts provider.Facts, logs provider.Logs, seed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)) {
	t.Helper()

	target := targetOn(facts, "conformance-history")
	entries := make([]provider.LogEntry, 5)
	for i := range entries {
		entries[i] = provider.LogEntry{Time: logsEpoch.Add(time.Duration(i) * time.Minute), Message: fmt.Sprintf("line %d", i)}
	}
	seed(t, target, entries)

	read := func(t *testing.T, q provider.LogQuery) []string {
		t.Helper()
		var messages []string
		err := logs.Read(context.Background(), q, func(batch []provider.LogEntry) error {
			for _, entry := range batch {
				messages = append(messages, entry.Message)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		return messages
	}

	t.Run("a read returns the newest Limit entries, oldest first", func(t *testing.T) {
		q := logQuery(target)
		q.Limit = 3
		if got, want := read(t, q), []string{"line 2", "line 3", "line 4"}; !slices.Equal(got, want) {
			t.Errorf("Read() with Limit 3 = %v, want %v", got, want)
		}
	})

	t.Run("a read returns the newest Limit entries between Since and Until", func(t *testing.T) {
		q := logQuery(target)
		q.Since, q.Until, q.Limit = logsEpoch.Add(time.Minute), logsEpoch.Add(3*time.Minute), 2
		if got, want := read(t, q), []string{"line 2", "line 3"}; !slices.Equal(got, want) {
			t.Errorf("Read() of Limit 2 from %s to %s = %v, want %v", q.Since, q.Until, got, want)
		}
	})

	t.Run("a read returns the newest Limit entries that contain the text", func(t *testing.T) {
		q := logQuery(target)
		q.Contains, q.Limit = "line 1", 1
		if got, want := read(t, q), []string{"line 1"}; !slices.Equal(got, want) {
			t.Errorf("Read() of Limit 1 containing %q = %v, want %v", q.Contains, got, want)
		}
	})

	t.Run("a read with a Limit below 1 returns nothing and fails nothing", func(t *testing.T) {
		q := logQuery(target)
		q.Limit = 0
		if got := read(t, q); len(got) != 0 {
			t.Errorf("Read() with Limit 0 = %v, want nothing", got)
		}
	})
}
