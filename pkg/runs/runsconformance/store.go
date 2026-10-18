package runsconformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runs"
)

const byteExactPayload = `{"ratio":2.0,"id":9007199254740993,"count":2}`

func Store(t *testing.T, open func(t *testing.T) runs.Store) {
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

	t.Run("a record is replaced only while it holds the value it was read with, and deleted only while it holds the value deleted", func(t *testing.T) {
		store := open(t)
		record := provider.ExpiringRecord{Purpose: provider.RecordDebounce, Topic: "digest", Key: "user-8", Value: json.RawMessage(`"a"`), ExpiresAt: time.Now().Add(time.Hour)}
		if _, _, err := store.EnsureRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
		rewritten := record
		rewritten.Value = json.RawMessage(`"b"`)
		if err := store.ReplaceRecord(ctx, rewritten, record.Value); err != nil {
			t.Fatalf("ReplaceRecord over the value the record holds: %v", err)
		}
		stale := record
		stale.Value = json.RawMessage(`"c"`)
		if err := store.ReplaceRecord(ctx, stale, record.Value); !errors.Is(err, records.ErrStale) {
			t.Fatalf("ReplaceRecord over a value the record no longer holds = %v, want ErrStale", err)
		}
		missing := record
		missing.Key = "user-never"
		if err := store.ReplaceRecord(ctx, missing, record.Value); !errors.Is(err, records.ErrStale) {
			t.Fatalf("ReplaceRecord of a key no record holds = %v, want ErrStale", err)
		}
		if err := store.DeleteRecord(ctx, record); err != nil {
			t.Fatalf("DeleteRecord of a value the record no longer holds: %v", err)
		}
		held, created, err := store.EnsureRecord(ctx, record)
		if err != nil || created || string(held.Value) != `"b"` {
			t.Fatalf("EnsureRecord after a stale replace and delete = %s, %v, %v, want the replaced record kept", held.Value, created, err)
		}
		if err := store.DeleteRecord(ctx, rewritten); err != nil {
			t.Fatalf("DeleteRecord: %v", err)
		}
		if _, created, err := store.EnsureRecord(ctx, record); err != nil || !created {
			t.Errorf("EnsureRecord after deleting the record = %v, %v, want it created again", created, err)
		}
	})

	t.Run("of concurrent replaces over the value a record holds, exactly one takes it", func(t *testing.T) {
		store := open(t)
		record := provider.ExpiringRecord{Purpose: provider.RecordDebounce, Topic: "digest", Key: "user-10", Value: json.RawMessage(`{"run":"started"}`), ExpiresAt: time.Now().Add(time.Hour)}
		if _, _, err := store.EnsureRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
		const takers = 8
		errs := make([]error, takers)
		var taking sync.WaitGroup
		for i := range takers {
			taking.Go(func() {
				taken := record
				taken.Value = json.RawMessage(fmt.Sprintf(`{"run":"taker-%d"}`, i))
				errs[i] = store.ReplaceRecord(ctx, taken, record.Value)
			})
		}
		taking.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil && winner < 0:
				winner = i
			case err == nil:
				t.Fatalf("takers %d and %d both replaced the record", winner, i)
			case !errors.Is(err, records.ErrStale):
				t.Fatalf("ReplaceRecord by taker %d: %v", i, err)
			}
		}
		if winner < 0 {
			t.Fatal("no taker replaced the record")
		}
		held, _, err := store.EnsureRecord(ctx, record)
		if err != nil || string(held.Value) != fmt.Sprintf(`{"run":"taker-%d"}`, winner) {
			t.Errorf("the record holds %s, %v, want the winning taker %d", held.Value, err, winner)
		}
	})

	t.Run("a run is created once and then written only over the revision it was read at", func(t *testing.T) {
		store := open(t)
		run := newRun("01K0000000000000000000000A-resize", "resize", provider.RunQueued, time.Now())

		first, err := store.CreateRun(ctx, run)
		if err != nil {
			t.Fatalf("CreateRun of a new run: %v", err)
		}
		if _, err := store.CreateRun(ctx, run); !errors.Is(err, records.ErrStale) {
			t.Fatalf("CreateRun of a run that exists = %v, want ErrStale", err)
		}
		if _, err := store.WriteRun(ctx, run); !errors.Is(err, records.ErrStale) {
			t.Fatalf("WriteRun with no revision = %v, want ErrStale", err)
		}

		read, err := store.ReadRun(ctx, run.Topic, run.Execution)
		if err != nil {
			t.Fatal(err)
		}
		if read.Revision != first || read.Status != provider.RunQueued || !read.CreatedAt.Equal(run.CreatedAt) {
			t.Fatalf("ReadRun = %+v, want the run as created at revision %s", read, first)
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
		if _, err := store.WriteRun(ctx, read); !errors.Is(err, records.ErrStale) {
			t.Errorf("WriteRun at a revision that moved = %v, want ErrStale", err)
		}

		again, err := store.ReadRun(ctx, run.Topic, run.Execution)
		if err != nil {
			t.Fatal(err)
		}
		if again.Status != provider.RunCompleted || string(again.Output) != `{"ok":true}` {
			t.Errorf("ReadRun after the update = %s %s, want completed with its output", again.Status, again.Output)
		}
	})

	t.Run("a run is deleted only at the revision it was read at", func(t *testing.T) {
		store := open(t)
		run := newRun("01K0000000000000000000000C-resize", "resize", provider.RunFailed, time.Now())
		revision, err := store.CreateRun(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteRun(ctx, run); !errors.Is(err, records.ErrStale) {
			t.Fatalf("DeleteRun with no revision = %v, want ErrStale", err)
		}
		run.Revision = revision
		if err := store.DeleteRun(ctx, run); err != nil {
			t.Fatalf("DeleteRun at the revision created: %v", err)
		}
		if _, err := store.ReadRun(ctx, run.Topic, run.Execution); !errors.Is(err, records.ErrNotFound) {
			t.Errorf("ReadRun after DeleteRun = %v, want ErrNotFound", err)
		}
	})

	t.Run("a run keeps every field it was written with", func(t *testing.T) {
		store := open(t)
		at := time.Now().UTC().Truncate(time.Microsecond)
		run := runs.Run{
			Execution:   "01K0000000000000000000000B-resize",
			Topic:       "resize",
			Consumer:    "resize",
			Status:      provider.RunExecuting,
			Payload:     json.RawMessage(byteExactPayload),
			Output:      json.RawMessage(byteExactPayload),
			Error:       "the worker answered 500",
			Attempts:    3,
			MaxAttempts: 5,
			Tags:        []string{"eu", "big"},
			Metadata:    json.RawMessage(`{"by":"cron"}`),
			Message:     "01K0000000000000000000000B",
			PublishedAt: at.Add(-time.Second),
			Key:         "order/7",
			Lane:        provider.LaneHigh,
			Delivery:    "01K0000000000000000000000D",
			Held:        "receipt-1",
			LeaseUntil:  at.Add(time.Minute),
			CreatedAt:   at,
			DueAt:       at.Add(time.Second),
			StartedAt:   at.Add(2 * time.Second),
			FinishedAt:  at.Add(3 * time.Second),
			ExpiresAt:   at.Add(time.Hour),
		}
		if _, err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		read, err := store.ReadRun(ctx, run.Topic, run.Execution)
		if err != nil {
			t.Fatal(err)
		}
		if string(read.Payload) != byteExactPayload || string(read.Output) != byteExactPayload {
			t.Errorf("ReadRun payload %s and output %s, want %s byte for byte: 2.0 stays a float, 2^53+1 is not rounded and key order holds", read.Payload, read.Output, byteExactPayload)
		}
		read.Payload, read.Output, read.Revision = run.Payload, run.Output, ""
		if diff := differences(read, run); len(diff) > 0 {
			t.Errorf("ReadRun differs from the run written in %v", diff)
		}
	})

	t.Run("reading a run no one wrote is not found", func(t *testing.T) {
		if _, err := open(t).ReadRun(ctx, "resize", "nothing"); !errors.Is(err, records.ErrNotFound) {
			t.Errorf("ReadRun = %v, want ErrNotFound", err)
		}
	})

	t.Run("runs are listed newest first by topic, consumer, status and tags a page at a time", func(t *testing.T) {
		store := open(t)
		start := time.Now().Add(-time.Hour)
		for i, run := range []runs.Run{
			newRun("a", "resize", provider.RunCompleted, start, "eu"),
			newRun("b", "resize", provider.RunFailed, start.Add(time.Minute), "eu", "big"),
			newRun("c", "thumbnail", provider.RunCompleted, start.Add(2*time.Minute), "eu"),
			newRun("d", "resize", provider.RunCompleted, start.Add(3*time.Minute), "us"),
			newRun("e", "resize", provider.RunCompleted, start.Add(4*time.Minute), "eu", "big"),
			withConsumer(newRun("f", "resize", provider.RunFailed, start.Add(5*time.Minute)), "audit"),
		} {
			if _, err := store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun %d: %v", i, err)
			}
		}

		for _, tc := range []struct {
			name   string
			filter runs.RunFilter
			want   []string
		}{
			{"by topic", runs.RunFilter{Topics: []string{"resize"}}, []string{"f", "e", "d", "b", "a"}},
			{"by consumer", runs.RunFilter{Topics: []string{"resize"}, Consumer: "resize"}, []string{"e", "d", "b", "a"}},
			{"by status", runs.RunFilter{Topics: []string{"resize"}, Statuses: []provider.RunStatus{provider.RunFailed}}, []string{"f", "b"}},
			{"by every tag", runs.RunFilter{Topics: []string{"resize", "thumbnail"}, Tags: []string{"eu", "big"}}, []string{"e", "b"}},
			{"by several topics", runs.RunFilter{Topics: []string{"resize", "thumbnail"}}, []string{"f", "e", "d", "c", "b", "a"}},
			{"by a topic with no runs", runs.RunFilter{Topics: []string{"digest"}}, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := listEveryRun(t, store, tc.filter, 2); !slices.Equal(got, tc.want) {
					t.Errorf("ListRuns(%+v) = %v, want %v", tc.filter, got, tc.want)
				}
				count, err := store.CountRuns(ctx, tc.filter)
				if err != nil || count != int64(len(tc.want)) {
					t.Errorf("CountRuns(%+v) = %d, %v, want %d", tc.filter, count, err, len(tc.want))
				}
			})
		}
	})

	t.Run("a cursor no listing returned is refused", func(t *testing.T) {
		if _, err := open(t).ListRuns(ctx, runs.RunFilter{Topics: []string{"resize"}, Cursor: "not-a-cursor"}); !errors.Is(err, runs.ErrUnknownCursor) {
			t.Errorf("ListRuns with a made-up cursor = %v, want %v", err, runs.ErrUnknownCursor)
		}
	})
}

func newRun(execution, topic string, status provider.RunStatus, created time.Time, tags ...string) runs.Run {
	return runs.Run{
		Execution:   execution,
		Topic:       topic,
		Consumer:    topic,
		Status:      status,
		Payload:     json.RawMessage(`{"n":1}`),
		MaxAttempts: 3,
		Tags:        tags,
		CreatedAt:   created.UTC().Truncate(time.Microsecond),
		DueAt:       created.UTC().Truncate(time.Microsecond),
	}
}

func withConsumer(run runs.Run, consumer string) runs.Run {
	run.Consumer = consumer
	return run
}

func differences(got, want runs.Run) []string {
	var differ []string
	check := func(field string, same bool) {
		if !same {
			differ = append(differ, field)
		}
	}
	check("execution", got.Execution == want.Execution)
	check("topic", got.Topic == want.Topic)
	check("consumer", got.Consumer == want.Consumer)
	check("status", got.Status == want.Status)
	check("error", got.Error == want.Error)
	check("attempts", got.Attempts == want.Attempts)
	check("maxAttempts", got.MaxAttempts == want.MaxAttempts)
	check("tags", slices.Equal(got.Tags, want.Tags))
	check("metadata", string(got.Metadata) == string(want.Metadata))
	check("message", got.Message == want.Message)
	check("publishedAt", got.PublishedAt.Equal(want.PublishedAt))
	check("key", got.Key == want.Key)
	check("lane", got.Lane == want.Lane)
	check("delivery", got.Delivery == want.Delivery)
	check("held", got.Held == want.Held)
	check("leaseUntil", got.LeaseUntil.Equal(want.LeaseUntil))
	check("createdAt", got.CreatedAt.Equal(want.CreatedAt))
	check("dueAt", got.DueAt.Equal(want.DueAt))
	check("startedAt", got.StartedAt.Equal(want.StartedAt))
	check("finishedAt", got.FinishedAt.Equal(want.FinishedAt))
	check("expiresAt", got.ExpiresAt.Equal(want.ExpiresAt))
	return differ
}

func listEveryRun(t *testing.T, store runs.Store, filter runs.RunFilter, limit int) []string {
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
