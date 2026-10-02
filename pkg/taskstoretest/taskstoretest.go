package taskstoretest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const byteExactPayload = `{"ratio":2.0,"id":9007199254740993,"count":2}`

func Run(t *testing.T, open func(t *testing.T) provider.TaskStore) {
	t.Helper()

	ctx := context.Background()

	t.Run("a record is created only while no live one holds its key", func(t *testing.T) {
		store := open(t)
		first := provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: "resize", Key: "order-1", Value: json.RawMessage(`{"run":"a"}`), ExpiresAt: time.Now().Add(time.Hour)}

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
		otherPurpose := second
		otherPurpose.Purpose = provider.RecordDebounce
		if _, created, err := store.EnsureRecord(ctx, otherPurpose); err != nil || !created {
			t.Errorf("EnsureRecord for the same key and topic with another purpose = %v, %v, want it created", created, err)
		}
	})

	t.Run("a key holding a slash or any byte is its own record", func(t *testing.T) {
		store := open(t)
		for _, key := range []string{"a/b", "a|b", "../..", "ключ", "a#b"} {
			record := provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: "resize", Key: key, Value: json.RawMessage(`"x"`), ExpiresAt: time.Now().Add(time.Hour)}
			if _, created, err := store.EnsureRecord(ctx, record); err != nil || !created {
				t.Errorf("EnsureRecord with key %q = %v, %v, want it created apart from every other key", key, created, err)
			}
		}
	})

	t.Run("an expired record gives way to the next one", func(t *testing.T) {
		store := open(t)
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
	})

	t.Run("a run is written once and then only over the revision it was read at", func(t *testing.T) {
		store := open(t)
		run := newRun("01K0000000000000000000000A-resize", "resize", provider.RunQueued, time.Now())

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
	})

	t.Run("a run keeps every field it was written with", func(t *testing.T) {
		store := open(t)
		at := time.Now().UTC().Truncate(time.Microsecond)
		run := provider.Run{
			Execution:  "01K0000000000000000000000B-resize",
			Topic:      "resize",
			Consumer:   "resize",
			Status:     provider.RunFailed,
			Payload:    json.RawMessage(byteExactPayload),
			Output:     json.RawMessage(byteExactPayload),
			Error:      "the worker answered 500",
			Attempts:   3,
			Tags:       []string{"eu", "big"},
			Metadata:   json.RawMessage(`{"by":"cron"}`),
			CreatedAt:  at,
			DueAt:      at.Add(time.Second),
			StartedAt:  at.Add(2 * time.Second),
			FinishedAt: at.Add(3 * time.Second),
			ExpiresAt:  at.Add(time.Hour),
		}
		if _, err := store.WriteRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		read, err := store.ReadRun(ctx, run.Execution)
		if err != nil {
			t.Fatal(err)
		}
		if string(read.Payload) != byteExactPayload || string(read.Output) != byteExactPayload {
			t.Errorf("ReadRun payload %s and output %s, want %s byte for byte: 2.0 stays a float, 2^53+1 is not rounded and key order holds", read.Payload, read.Output, byteExactPayload)
		}
		if read.Error != run.Error || read.Attempts != run.Attempts || !slices.Equal(read.Tags, run.Tags) || string(read.Metadata) != string(run.Metadata) || read.Consumer != run.Consumer {
			t.Errorf("ReadRun = %+v, want %+v", read, run)
		}
		for name, pair := range map[string][2]time.Time{
			"due": {read.DueAt, run.DueAt}, "started": {read.StartedAt, run.StartedAt},
			"finished": {read.FinishedAt, run.FinishedAt}, "expires": {read.ExpiresAt, run.ExpiresAt},
		} {
			if !pair[0].Equal(pair[1]) {
				t.Errorf("ReadRun %s at %v, want %v", name, pair[0], pair[1])
			}
		}
	})

	t.Run("reading a run no one wrote is not found", func(t *testing.T) {
		if _, err := open(t).ReadRun(ctx, "nothing"); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Errorf("ReadRun = %v, want ErrNotFound", err)
		}
	})

	t.Run("runs are listed newest first by topic, status and tags a page at a time", func(t *testing.T) {
		store := open(t)
		start := time.Now().Add(-time.Hour)
		for i, run := range []provider.Run{
			newRun("a", "resize", provider.RunCompleted, start, "eu"),
			newRun("b", "resize", provider.RunFailed, start.Add(time.Minute), "eu", "big"),
			newRun("c", "thumbnail", provider.RunCompleted, start.Add(2*time.Minute), "eu"),
			newRun("d", "resize", provider.RunCompleted, start.Add(3*time.Minute), "us"),
			newRun("e", "resize", provider.RunCompleted, start.Add(4*time.Minute), "eu", "big"),
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
				if got := listEveryRun(t, store, tc.filter, 2); !slices.Equal(got, tc.want) {
					t.Errorf("ListRuns(%+v) = %v, want %v", tc.filter, got, tc.want)
				}
			})
		}
	})

	t.Run("a cursor no listing returned is refused", func(t *testing.T) {
		if _, err := open(t).ListRuns(ctx, provider.RunFilter{Cursor: "not-a-cursor"}); !errors.Is(err, runs.ErrUnknownCursor) {
			t.Errorf("ListRuns with a made-up cursor = %v, want %v", err, runs.ErrUnknownCursor)
		}
	})
}

func newRun(execution, topic string, status provider.RunStatus, created time.Time, tags ...string) provider.Run {
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

func listEveryRun(t *testing.T, store provider.TaskStore, filter provider.RunFilter, limit int) []string {
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
