package pgmq

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
)

func anEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := Open(context.Background(), aDatabase(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func TestARecordIsCreatedOnlyWhileNoLiveOneHoldsItsKey(t *testing.T) {
	ctx := context.Background()
	store := anEngine(t).Store()
	now := time.Now()
	first := provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: "resize", Key: "order-1", Value: json.RawMessage(`{"run":"a"}`), ExpiresAt: now.Add(time.Hour)}

	got, created, err := store.EnsureRecord(ctx, first)
	if err != nil || !created {
		t.Fatalf("EnsureRecord on an empty store = %v, %v, want it created", created, err)
	}
	second := first
	second.Value = json.RawMessage(`{"run":"b"}`)
	got, created, err = store.EnsureRecord(ctx, second)
	if err != nil || created {
		t.Fatalf("EnsureRecord over a live record = %v, %v, want the live one kept", created, err)
	}
	if string(got.Value) != `{"run":"a"}` {
		t.Errorf("EnsureRecord returned %s, want the first record's value", got.Value)
	}

	otherTopic := second
	otherTopic.Topic = "thumbnail"
	if _, created, err := store.EnsureRecord(ctx, otherTopic); err != nil || !created {
		t.Errorf("EnsureRecord for the same key on another topic = %v, %v, want it created", created, err)
	}
}

func TestAnExpiredRecordGivesWayToTheNextOne(t *testing.T) {
	ctx := context.Background()
	store := anEngine(t).Store()
	expired := provider.ExpiringRecord{Purpose: provider.RecordDebounce, Topic: "digest", Key: "user-7", Value: json.RawMessage(`"old"`), ExpiresAt: time.Now().Add(-time.Second)}
	if _, _, err := store.EnsureRecord(ctx, expired); err != nil {
		t.Fatal(err)
	}

	next := expired
	next.Value = json.RawMessage(`"new"`)
	next.ExpiresAt = time.Now().Add(time.Hour)
	got, created, err := store.EnsureRecord(ctx, next)
	if err != nil || !created {
		t.Fatalf("EnsureRecord over an expired record = %v, %v, want it created", created, err)
	}
	if string(got.Value) != `"new"` {
		t.Errorf("EnsureRecord returned %s, want the new value", got.Value)
	}
}

func aRun(execution, topic string, status provider.RunStatus, created time.Time, tags ...string) provider.Run {
	return provider.Run{
		Execution: execution,
		Topic:     topic,
		Consumer:  topic,
		Status:    status,
		Payload:   json.RawMessage(`{"n":1}`),
		Tags:      tags,
		CreatedAt: created.UTC().Truncate(time.Microsecond),
	}
}

func TestARunIsWrittenOnceAndThenOnlyOverTheRevisionItWasReadAt(t *testing.T) {
	ctx := context.Background()
	store := anEngine(t).Store()
	run := aRun("01K0000000000000000000000A-resize", "resize", provider.RunQueued, time.Now())

	first, err := store.WriteRun(ctx, run)
	if err != nil {
		t.Fatalf("WriteRun of a new run: %v", err)
	}
	if _, err := store.WriteRun(ctx, run); !errors.Is(err, keyvalue.ErrStale) {
		t.Fatalf("WriteRun of a run that exists, with no revision = %v, want ErrStale", err)
	}

	read, err := store.ReadRun(ctx, run.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != first || read.Status != provider.RunQueued || !read.CreatedAt.Equal(run.CreatedAt) {
		t.Fatalf("ReadRun = %+v, want the run as written at revision %s", read, first)
	}
	read.Status = provider.RunCompleted
	read.Output = json.RawMessage(`{"ok":true}`)
	second, err := store.WriteRun(ctx, read)
	if err != nil {
		t.Fatalf("WriteRun at the revision read: %v", err)
	}
	if second == first {
		t.Error("WriteRun kept the revision, so a writer holding the old one could overwrite this")
	}
	if _, err := store.WriteRun(ctx, read); !errors.Is(err, keyvalue.ErrStale) {
		t.Errorf("WriteRun at a revision that moved = %v, want ErrStale", err)
	}

	again, err := store.ReadRun(ctx, run.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != provider.RunCompleted || string(again.Output) != `{"ok":true}` {
		t.Errorf("ReadRun after the update = %s %s, want completed with its output", again.Status, again.Output)
	}
}

func TestReadingARunNoOneWroteIsNotFound(t *testing.T) {
	if _, err := anEngine(t).Store().ReadRun(context.Background(), "nothing"); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("ReadRun = %v, want ErrNotFound", err)
	}
}

func TestRunsAreListedNewestFirstByTopicStatusAndTagsAPageAtATime(t *testing.T) {
	ctx := context.Background()
	store := anEngine(t).Store()
	start := time.Now().Add(-time.Hour)
	for i, run := range []provider.Run{
		aRun("a", "resize", provider.RunCompleted, start, "eu"),
		aRun("b", "resize", provider.RunFailed, start.Add(time.Minute), "eu", "big"),
		aRun("c", "thumbnail", provider.RunCompleted, start.Add(2*time.Minute), "eu"),
		aRun("d", "resize", provider.RunCompleted, start.Add(3*time.Minute), "us"),
		aRun("e", "resize", provider.RunCompleted, start.Add(4*time.Minute), "eu", "big"),
	} {
		if _, err := store.WriteRun(ctx, run); err != nil {
			t.Fatalf("WriteRun %d: %v", i, err)
		}
	}

	for _, tc := range []struct {
		name   string
		filter provider.RunFilter
		want   []string
	}{
		{"by topic", provider.RunFilter{Topic: "resize"}, []string{"e", "d", "b", "a"}},
		{"by status", provider.RunFilter{Topic: "resize", Statuses: []provider.RunStatus{provider.RunFailed}}, []string{"b"}},
		{"by every tag", provider.RunFilter{Tags: []string{"eu", "big"}}, []string{"e", "b"}},
		{"every topic", provider.RunFilter{}, []string{"e", "d", "c", "b", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := listAll(t, store, tc.filter, 2); !slices.Equal(got, tc.want) {
				t.Errorf("ListRuns(%+v) = %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

func listAll(t *testing.T, store provider.TaskStore, filter provider.RunFilter, limit int) []string {
	t.Helper()
	filter.Limit = limit
	var executions []string
	for range 10 {
		page, err := store.ListRuns(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Runs) > limit {
			t.Fatalf("ListRuns returned %d runs, over the limit %d", len(page.Runs), limit)
		}
		for _, run := range page.Runs {
			executions = append(executions, run.Execution)
		}
		if page.NextCursor == "" {
			return executions
		}
		filter.Cursor = page.NextCursor
	}
	t.Fatal("ListRuns never ran out of pages")
	return nil
}
