package vps_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func loggingBox(logs map[string]session.Result) *box {
	return &box{refuses: func(command string) (session.Result, bool) {
		if !strings.Contains(command, "docker logs") {
			return session.Result{}, false
		}
		for name, result := range logs {
			if strings.Contains(command, "'"+name+"'") {
				return result, true
			}
		}
		return session.Result{}, false
	}}
}

func TestLogsReadsEachContainerOfTheTargetsByItsPhysicalName(t *testing.T) {
	t.Parallel()

	machine := loggingBox(map[string]session.Result{
		"shop-web-r1": {
			Stdout: "2026-01-05T12:00:01Z served /\n",
			Stderr: "2026-01-05T12:00:03Z warn: slow\n",
		},
		"shop-media-r1": {Stdout: "2026-01-05T12:00:02Z resized\n"},
	})
	query := provider.LogQuery{
		Targets: []provider.LogTarget{
			{App: "web", Release: "r1", Source: "http", Container: &provider.AppContainer{Name: "web", Physical: "shop-web-r1"}},
			{App: "web", Release: "r1", Source: "media", Container: &provider.AppContainer{Name: "worker:media", Physical: "shop-media-r1"}},
		},
		Since: time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit: 2,
	}

	var entries []provider.LogEntry
	err := over(machine).Logs().Read(context.Background(), query, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	want := []provider.LogEntry{
		{Time: time.Date(2026, 1, 5, 12, 0, 2, 0, time.UTC), App: "web", Release: "r1", Source: "media", Stream: provider.LogStreamStdout, Message: "resized"},
		{Time: time.Date(2026, 1, 5, 12, 0, 3, 0, time.UTC), App: "web", Release: "r1", Source: "http", Stream: provider.LogStreamStderr, Message: "warn: slow"},
	}
	if len(entries) != len(want) {
		t.Fatalf("Read() emitted %+v, want the newest 2 across both containers: %+v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("Read() entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestLogsNamesTheTargetsWhoseContainerNoLongerExists(t *testing.T) {
	t.Parallel()

	machine := loggingBox(map[string]session.Result{
		"shop-web-r1": {Code: 1, Stderr: "Error response from daemon: No such container: shop-web-r1"},
		"shop-web-r2": {Stdout: "2026-01-05T12:00:01Z served /\n"},
	})
	retired := provider.LogTarget{App: "web", Release: "r1", Source: "http", Container: &provider.AppContainer{Name: "web", Physical: "shop-web-r1"}}
	live := provider.LogTarget{App: "web", Release: "r2", Source: "http", Container: &provider.AppContainer{Name: "web", Physical: "shop-web-r2"}}

	var entries []provider.LogEntry
	err := over(machine).Logs().Read(context.Background(), provider.LogQuery{
		Targets: []provider.LogTarget{retired, live},
		Since:   time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:   10,
	}, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		return nil
	}, nil)

	var missing provider.LogTargetsMissing
	if !errors.As(err, &missing) || len(missing.Targets) != 1 || missing.Targets[0].Release != "r1" {
		t.Fatalf("Read() error = %v, want the retired release named as missing", err)
	}
	if len(entries) != 1 || entries[0].Release != "r2" {
		t.Errorf("Read() emitted %+v, want the live release's entry read all the same", entries)
	}
}

func TestLogsReadsNothingFromAnEmptyTargetList(t *testing.T) {
	t.Parallel()

	machine := &box{}
	err := over(machine).Logs().Read(context.Background(), provider.LogQuery{Limit: 10}, func([]provider.LogEntry) error {
		t.Error("Read() emitted a batch for no targets")
		return nil
	}, nil)
	if err != nil {
		t.Errorf("Read() error = %v, want none", err)
	}
	if len(machine.commands()) != 0 {
		t.Errorf("Read() ran %v for no targets, want no command", machine.commands())
	}
}

func TestLogsKeepsOnlyTheLinesThatContainTheText(t *testing.T) {
	t.Parallel()

	machine := loggingBox(map[string]session.Result{
		"shop-web-r1": {Stdout: "2026-01-05T12:00:01Z boom one\n2026-01-05T12:00:02Z fine\n"},
	})
	var entries []provider.LogEntry
	err := over(machine).Logs().Read(context.Background(), provider.LogQuery{
		Targets:  []provider.LogTarget{{App: "web", Release: "r1", Source: "http", Container: &provider.AppContainer{Name: "web", Physical: "shop-web-r1"}}},
		Since:    time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:    2,
		Contains: "boom",
	}, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Message != "boom one" {
		t.Errorf("Read() emitted %+v, want only the line containing the text", entries)
	}
	if command := machine.commands()[len(machine.commands())-1]; strings.Contains(command, "--tail") {
		t.Errorf("Read() ran %q, and a tail docker takes before the text is matched leaves fewer matches than there are", command)
	}
}

func TestLogsRefusesATargetThatIsNotAContainer(t *testing.T) {
	t.Parallel()

	err := over(&box{}).Logs().Read(context.Background(), provider.LogQuery{
		Targets: []provider.LogTarget{{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web", Physical: "web-fn"}}},
		Since:   time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:   10,
	}, func([]provider.LogEntry) error { return nil }, nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Read() error = %v, want an invalid refusal", err)
	}
}

var tailSince = time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC)

func tailTarget(release, source, physical string) provider.LogTarget {
	return provider.LogTarget{App: "web", Release: release, Source: source, Container: &provider.AppContainer{Name: physical, Physical: physical}}
}

func followedBy(lines map[string][]session.Line) func(context.Context, string, func(session.Line) error) error {
	return func(ctx context.Context, command string, each func(session.Line) error) error {
		for name, followed := range lines {
			if !strings.Contains(command, "'"+name+"'") {
				continue
			}
			for _, line := range followed {
				if err := each(line); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

type tailedRead struct {
	entries []provider.LogEntry
	err     error
}

func tailRead(t *testing.T, machine *box, query provider.LogQuery) tailedRead {
	t.Helper()
	query.Tail = true
	if query.Since.IsZero() {
		query.Since = tailSince
	}
	var (
		mu   sync.Mutex
		read tailedRead
	)
	done := make(chan error, 1)
	go func() {
		done <- over(machine).Logs().Read(context.Background(), query, func(batch []provider.LogEntry) error {
			mu.Lock()
			defer mu.Unlock()
			read.entries = append(read.entries, batch...)
			return nil
		}, func(provider.LogNotice) error { return nil })
	}()
	select {
	case read.err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Read() of a tail whose containers had all stopped was still running after 5s")
	}
	return read
}

func TestLogsTailsEachContainerFromSinceAndKeepsStdoutAndStderrApart(t *testing.T) {
	t.Parallel()

	machine := &box{follows: followedBy(map[string][]session.Line{
		"shop-web-r1":   {{Pipe: session.Stdout, Text: "2026-01-05T12:00:01Z served /"}, {Pipe: session.Stderr, Text: "2026-01-05T12:00:03Z warn: slow"}},
		"shop-media-r1": {{Pipe: session.Stdout, Text: "2026-01-05T12:00:02Z resized"}},
	})}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r1", "media", "shop-media-r1"),
	}})
	if read.err != nil {
		t.Fatalf("Read() of a tail error = %v", read.err)
	}

	got := map[string]provider.LogEntry{}
	for _, entry := range read.entries {
		got[entry.Message] = entry
	}
	want := map[string]provider.LogEntry{
		"served /":   {Time: time.Date(2026, 1, 5, 12, 0, 1, 0, time.UTC), App: "web", Release: "r1", Source: "http", Stream: provider.LogStreamStdout, Message: "served /"},
		"warn: slow": {Time: time.Date(2026, 1, 5, 12, 0, 3, 0, time.UTC), App: "web", Release: "r1", Source: "http", Stream: provider.LogStreamStderr, Message: "warn: slow"},
		"resized":    {Time: time.Date(2026, 1, 5, 12, 0, 2, 0, time.UTC), App: "web", Release: "r1", Source: "media", Stream: provider.LogStreamStdout, Message: "resized"},
	}
	if len(got) != len(want) {
		t.Fatalf("Read() of a tail emitted %+v, want %+v", read.entries, want)
	}
	for message, entry := range want {
		if got[message] != entry {
			t.Errorf("Read() of a tail emitted %+v for %q, want %+v", got[message], message, entry)
		}
	}

	var followed []string
	for _, command := range machine.commands() {
		if strings.Contains(command, "docker logs") {
			followed = append(followed, command)
		}
	}
	if len(followed) != 2 {
		t.Fatalf("Read() of a tail ran %v, want one docker logs per container", followed)
	}
	for _, command := range followed {
		for _, wanted := range []string{"--follow", "--since '2026-01-05T11:00:00Z'"} {
			if !strings.Contains(command, wanted) {
				t.Errorf("Read() of a tail ran %q, want it to contain %q", command, wanted)
			}
		}
	}
}

func TestLogsTailFollowsEveryContainerAtOnce(t *testing.T) {
	t.Parallel()

	var started sync.WaitGroup
	started.Add(2)
	machine := &box{follows: func(ctx context.Context, command string, each func(session.Line) error) error {
		if !strings.Contains(command, "docker logs") {
			return nil
		}
		started.Done()
		started.Wait()
		return each(session.Line{Text: "2026-01-05T12:00:01Z up"})
	}}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r1", "media", "shop-media-r1"),
	}})
	if read.err != nil || len(read.entries) != 2 {
		t.Errorf("Read() of a tail = %d entries, %v, want a line from each of two containers followed side by side", len(read.entries), read.err)
	}
}

func TestLogsTailKeepsOnlyTheLinesThatContainTheText(t *testing.T) {
	t.Parallel()

	machine := &box{follows: followedBy(map[string][]session.Line{
		"shop-web-r1": {{Text: "2026-01-05T12:00:01Z boom one"}, {Text: "2026-01-05T12:00:02Z fine"}},
	})}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{tailTarget("r1", "http", "shop-web-r1")}, Contains: "boom"})
	if len(read.entries) != 1 || read.entries[0].Message != "boom one" {
		t.Errorf("Read() of a tail emitted %+v, want only the line containing the text", read.entries)
	}
}

func TestLogsTailsWhateverTheLimitIs(t *testing.T) {
	t.Parallel()

	machine := &box{follows: followedBy(map[string][]session.Line{
		"shop-web-r1": {{Text: "2026-01-05T12:00:01Z one"}, {Text: "2026-01-05T12:00:02Z two"}},
	})}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{tailTarget("r1", "http", "shop-web-r1")}})
	if len(read.entries) != 2 {
		t.Errorf("Read() of a tail with Limit 0 emitted %+v, want every line followed", read.entries)
	}
}

func TestLogsTailSkipsAContainerThatNoLongerExistsAndFollowsTheRest(t *testing.T) {
	t.Parallel()

	machine := &box{follows: func(ctx context.Context, command string, each func(session.Line) error) error {
		if strings.Contains(command, "'shop-web-r1'") {
			return errors.New("ada@box over ssh: Error response from daemon: No such container: shop-web-r1")
		}
		return each(session.Line{Text: "2026-01-05T12:00:01Z still here"})
	}}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r2", "http", "shop-web-r2"),
	}})
	if read.err != nil {
		t.Fatalf("Read() of a tail error = %v, want a missing container skipped", read.err)
	}
	if len(read.entries) != 1 || read.entries[0].Release != "r2" {
		t.Errorf("Read() of a tail emitted %+v, want the surviving release's line", read.entries)
	}
}

func TestLogsTailStopsEveryContainerWhenOneFailsAndReturnsTheFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("the connection dropped")
	machine := &box{follows: func(ctx context.Context, command string, each func(session.Line) error) error {
		if strings.Contains(command, "'shop-web-r1'") {
			return failure
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	read := tailRead(t, machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r1", "media", "shop-media-r1"),
	}})
	if !errors.Is(read.err, failure) {
		t.Errorf("Read() of a tail error = %v, want the failure of the container that broke, and the one still following stopped", read.err)
	}
}

func TestLogsTailStopsWhenTheCallerCancels(t *testing.T) {
	t.Parallel()

	machine := &box{follows: func(ctx context.Context, command string, each func(session.Line) error) error {
		if !strings.Contains(command, "docker logs") {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- over(machine).Logs().Read(ctx, provider.LogQuery{
			Targets: []provider.LogTarget{tailTarget("r1", "http", "shop-web-r1")},
			Since:   tailSince,
			Tail:    true,
		}, func([]provider.LogEntry) error { return nil }, func(provider.LogNotice) error { return nil })
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Read() of a cancelled tail error = %v, want none", err)
		}
	case <-time.After(time.Second):
		t.Error("Read() of a tail was still running 1s after its context was cancelled")
	}
}

func TestLogsTailsNothingFromAnEmptyTargetList(t *testing.T) {
	t.Parallel()

	machine := &box{}
	read := tailRead(t, machine, provider.LogQuery{})
	if read.err != nil || len(read.entries) != 0 {
		t.Errorf("Read() of a tail with no targets = %+v, %v, want nothing", read.entries, read.err)
	}
	if len(machine.commands()) != 0 {
		t.Errorf("Read() of a tail with no targets ran %v, want no command", machine.commands())
	}
}

func TestLogsTailRefusesATargetThatIsNotAContainer(t *testing.T) {
	t.Parallel()

	read := tailRead(t, &box{}, provider.LogQuery{
		Targets: []provider.LogTarget{{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web", Physical: "web-fn"}}},
	})
	var refused refusal.Refusal
	if !errors.As(read.err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Read() of a tail error = %v, want an invalid refusal", read.err)
	}
}
