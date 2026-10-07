package conformance

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type LogChecks struct {
	Seed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)
	Feed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)
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
	if suite.Logs != nil && suite.Logs.Feed != nil {
		RunLogTail(t, p.Facts(), p.Logs(), suite.Logs.Feed)
		t.Run("a tailing ReadLogs sends CAUGHT_UP before any live entry and stops within a second of its caller cancelling", func(t *testing.T) {
			RunLogTailRPC(t, p, suite.Options, suite.Logs.Feed)
		})
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
		if err := logs.Read(context.Background(), logQuery(), emit, noticeNothing); err != nil {
			t.Errorf("Read() with no targets error = %v, want none", err)
		}
	})

	t.Run("a tail of no targets reads nothing and fails nothing", func(t *testing.T) {
		q := logQuery()
		q.Tail, q.Since = true, time.Now()
		emit := func(entries []provider.LogEntry) error {
			t.Errorf("Read() of a tail with no targets emitted %v, want nothing", entries)
			return nil
		}
		if err := logs.Read(context.Background(), q, emit, noticeNothing); err != nil {
			t.Errorf("Read() of a tail with no targets error = %v, want none", err)
		}
	})

	t.Run("a tail returns within a second of its context being cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		q := logQuery(targetOn(facts, "conformance-cancelled"))
		q.Tail, q.Since = true, time.Now()
		done := make(chan struct{})
		var err error
		go func() {
			defer close(done)
			err = logs.Read(ctx, q, func([]provider.LogEntry) error { return nil }, noticeNothing)
		}()
		cancel()
		select {
		case <-done:
			if err != nil {
				t.Errorf("Read() of a tail its caller cancelled returned %v, want none: cancelling is how a tail ends", err)
			}
		case <-time.After(cancelledReadDeadline):
			t.Errorf("Read() of a tail was still running %s after its context was cancelled", cancelledReadDeadline)
		}
	})

	t.Run("a read returns within a second of its context being cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = logs.Read(ctx, logQuery(targetOn(facts, "conformance-cancelled")), func([]provider.LogEntry) error { return nil }, noticeNothing)
		}()
		select {
		case <-done:
		case <-time.After(cancelledReadDeadline):
			t.Errorf("Read() was still running %s after its context was cancelled", cancelledReadDeadline)
		}
	})
}

func noticeNothing(provider.LogNotice) error { return nil }

type tailRun struct {
	live chan provider.LogEntry
	done chan error
}

func startTail(ctx context.Context, logs provider.Logs, since time.Time, emit func([]provider.LogEntry) error, targets ...provider.LogTarget) tailRun {
	q := logQuery(targets...)
	q.Tail, q.Since, q.Limit = true, since, 0
	run := tailRun{live: make(chan provider.LogEntry, 64), done: make(chan error, 1)}
	if emit == nil {
		emit = func(batch []provider.LogEntry) error {
			for _, entry := range batch {
				run.live <- entry
			}
			return nil
		}
	}
	go func() { run.done <- logs.Read(ctx, q, emit, noticeNothing) }()
	return run
}

func (r tailRun) next(t *testing.T) (provider.LogEntry, bool) {
	t.Helper()
	select {
	case entry := <-r.live:
		return entry, true
	case err := <-r.done:
		t.Fatalf("a tail ended with %v before it emitted a live entry", err)
	case <-time.After(5 * time.Second):
		t.Error("a tail emitted nothing within 5s of an entry being written")
	}
	return provider.LogEntry{}, false
}

func (r tailRun) quiet(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case entry := <-r.live:
		t.Errorf("a tail emitted %q, want nothing", entry.Message)
	case err := <-r.done:
		t.Errorf("a tail ended with %v while nothing had cancelled it, want it still running", err)
	case <-time.After(within):
	}
}

func RunLogTail(t *testing.T, facts provider.Facts, logs provider.Logs, feed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)) {
	t.Helper()

	target := targetOn(facts, "conformance-tail")

	t.Run("a tail emits the entries written after it began, and returns within a second of its context being cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tail := startTail(ctx, logs, time.Now(), nil, target)
		feed(t, target, []provider.LogEntry{{Time: time.Now(), Message: "live"}})
		if entry, ok := tail.next(t); ok && entry.Message != "live" {
			t.Errorf("a tail emitted %q, want the entry written after it began", entry.Message)
		}
		cancel()
		select {
		case err := <-tail.done:
			if err != nil {
				t.Errorf("a tail returned %v after its context was cancelled, want none", err)
			}
		case <-time.After(cancelledReadDeadline):
			t.Errorf("a tail was still running %s after its context was cancelled", cancelledReadDeadline)
		}
	})

	t.Run("a tail and a read give an entry the same ID", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		named := targetOn(facts, "conformance-named")
		since := time.Now()
		tail := startTail(ctx, logs, since, nil, named)
		feed(t, named, []provider.LogEntry{{Time: time.Now(), Message: "named"}})
		tailed, ok := tail.next(t)
		if !ok {
			return
		}
		if tailed.ID == "" {
			t.Fatal("a tail emitted an entry with no ID, and without one the server cannot tell it from an entry history already sent")
		}
		q := logQuery(named)
		q.Since = since
		var read []provider.LogEntry
		if err := logs.Read(context.Background(), q, func(batch []provider.LogEntry) error {
			read = append(read, batch...)
			return nil
		}, noticeNothing); err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if len(read) != 1 || read[0].ID != tailed.ID {
			t.Errorf("a read gave the entry a tail emitted as %q the IDs %+v, want the same ID", tailed.ID, read)
		}
	})

	t.Run("a tail keeps running while nothing is written", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		startTail(ctx, logs, time.Now(), nil, targetOn(facts, "conformance-quiet")).quiet(t, 200*time.Millisecond)
	})

	t.Run("a tail emits nothing stamped before its Since", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		early := targetOn(facts, "conformance-early")
		since := time.Now()
		tail := startTail(ctx, logs, since, nil, early)
		feed(t, early, []provider.LogEntry{{Time: since.Add(-time.Minute), Message: "before since"}})
		feed(t, early, []provider.LogEntry{{Time: time.Now(), Message: "after since"}})
		if entry, ok := tail.next(t); ok && entry.Message != "after since" {
			t.Errorf("a tail from %s emitted %q stamped %s, want only entries stamped from its Since on", since, entry.Message, entry.Time)
		}
	})

	t.Run("a tail emits only its own targets' entries, and every tail of a target emits them", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		shared, other := targetOn(facts, "conformance-shared"), targetOn(facts, "conformance-other")
		first := startTail(ctx, logs, time.Now(), nil, shared)
		second := startTail(ctx, logs, time.Now(), nil, shared)
		elsewhere := startTail(ctx, logs, time.Now(), nil, other)
		feed(t, shared, []provider.LogEntry{{Time: time.Now(), Message: "shared"}})
		for name, tail := range map[string]tailRun{"the first": first, "the second": second} {
			if entry, ok := tail.next(t); ok && entry.Message != "shared" {
				t.Errorf("%s tail of a target emitted %q, want its entry", name, entry.Message)
			}
		}
		elsewhere.quiet(t, 200*time.Millisecond)
	})

	t.Run("a tail that fails to emit stops and returns the failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		refused := errors.New("the caller has gone away")
		tail := startTail(ctx, logs, time.Now(), func([]provider.LogEntry) error { return refused }, target)
		feed(t, target, []provider.LogEntry{{Time: time.Now(), Message: "live"}})
		select {
		case err := <-tail.done:
			if !errors.Is(err, refused) {
				t.Errorf("a tail whose emit failed returned %v, want that failure", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("a tail whose emit failed was still running 5s later")
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
		}, noticeNothing)
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

func RunLogTailRPC(t *testing.T, p provider.Provider, options provider.Options, feed func(t *testing.T, target provider.LogTarget, entries []provider.LogEntry)) {
	t.Helper()

	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "conformance",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}))
	t.Cleanup(server.Close)
	served := client(server.Client(), server.URL)
	if _, err := served.Configure(context.Background(), configureWith(t, options)); err != nil {
		t.Fatalf("Configure() error = %v, want the session configured", err)
	}
	target := recordLoggedApp(t, p, "conformance-rpc")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := served.ReadLogs(ctx, &contractv1.ReadLogsRequest{
		Slug:        "conformance",
		Since:       timestamppb.New(time.Now().Add(-time.Hour)),
		Limit:       100,
		AllReleases: true,
		Tail:        true,
	})
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	defer stream.Close()
	received := make(chan *contractv1.ReadLogsResponse)
	ended := make(chan error, 1)
	go func() {
		for stream.Receive() {
			select {
			case received <- stream.Msg():
			case <-ctx.Done():
			}
		}
		ended <- stream.Err()
	}()
	next := func(what string) *contractv1.ReadLogsResponse {
		t.Helper()
		select {
		case msg := <-received:
			return msg
		case err := <-ended:
			t.Fatalf("a tailing ReadLogs ended with %v before it sent %s", err, what)
		case <-time.After(5 * time.Second):
			t.Fatalf("a tailing ReadLogs sent no %s within 5s", what)
		}
		return nil
	}

	for {
		msg := next("CAUGHT_UP")
		if msg.GetBatch() != nil {
			continue
		}
		if kind := msg.GetNotice().GetKind(); kind != contractv1.LogNotice_KIND_CAUGHT_UP {
			t.Fatalf("a tailing ReadLogs sent the notice %s before CAUGHT_UP", kind)
		}
		break
	}
	feed(t, target, []provider.LogEntry{{Time: time.Now(), Message: "after caught up"}})
	if entries := next("the live entry").GetBatch().GetEntries(); len(entries) != 1 || entries[0].GetMessage() != "after caught up" {
		t.Errorf("a tailing ReadLogs sent %v after CAUGHT_UP, want the entry written then", entries)
	}

	cancel()
	select {
	case <-ended:
	case <-time.After(cancelledReadDeadline):
		t.Errorf("a tailing ReadLogs was still streaming %s after its caller cancelled", cancelledReadDeadline)
	}
}

func recordLoggedApp(t *testing.T, p provider.Provider, physical string) provider.LogTarget {
	t.Helper()
	release, err := provider.ParseRelease(fmt.Sprintf("%032x~%012x", 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	target := targetOn(p.Facts(), physical)
	stack := stackrecords.Stack{Kind: provider.StackApp, App: target.App, ReleaseToken: release.Token().String(), Release: release.String()}
	if target.Function != nil {
		stack.Functions = []provider.Function{*target.Function}
	} else {
		stack.Containers = []provider.AppContainer{*target.Container}
	}
	name := naming.AppStack(stackrecords.ProductionEnv, target.App, release.Token())
	if err := stackrecords.Write(context.Background(), p.KeyValues(), environment.TierProduction, "conformance", name, stack); err != nil {
		t.Fatalf("record the stack of %s: %v", target.App, err)
	}
	return target
}
