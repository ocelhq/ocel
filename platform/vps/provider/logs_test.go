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

func followedNames(command string) []string {
	_, args, _ := strings.Cut(command, "' containerlogs ")
	var names []string
	for _, field := range strings.Fields(args)[1:] {
		names = append(names, strings.Trim(field, "'"))
	}
	return names
}

func saying(lines ...session.Line) func(context.Context, string, func(session.Line) error) error {
	return func(ctx context.Context, _ string, each func(session.Line) error) error {
		for _, line := range lines {
			if err := each(line); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

type tailedRead struct {
	mu      sync.Mutex
	entries []provider.LogEntry
	notices []provider.LogNotice
	cancel  context.CancelFunc
	done    chan error
}

func tailOn(machine *box, query provider.LogQuery) *tailedRead {
	query.Tail = true
	if query.Since.IsZero() {
		query.Since = tailSince
	}
	ctx, cancel := context.WithCancel(context.Background())
	read := &tailedRead{cancel: cancel, done: make(chan error, 1)}
	go func() {
		read.done <- over(machine).Logs().Read(ctx, query, func(batch []provider.LogEntry) error {
			read.mu.Lock()
			defer read.mu.Unlock()
			read.entries = append(read.entries, batch...)
			return nil
		}, func(notice provider.LogNotice) error {
			read.mu.Lock()
			defer read.mu.Unlock()
			read.notices = append(read.notices, notice)
			return nil
		})
	}()
	return read
}

func (r *tailedRead) settle(t *testing.T, entries, notices int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		reached := len(r.entries) >= entries && len(r.notices) >= notices
		r.mu.Unlock()
		if reached {
			return
		}
	}
	t.Fatalf("a tail emitted %d entries and %d notices within 5s, want %d and %d", len(r.entries), len(r.notices), entries, notices)
}

func (r *tailedRead) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		return err
	case <-time.After(time.Second):
		t.Fatal("Read() of a tail was still running 1s after its context was cancelled")
	}
	return nil
}

func TestLogsTailsEveryContainerThroughOneFollowerFromSince(t *testing.T) {
	t.Parallel()

	machine := &box{follows: saying(
		session.Line{Pipe: session.Stdout, Text: "0 2026-01-05T12:00:01Z served /"},
		session.Line{Pipe: session.Stderr, Text: "0 2026-01-05T12:00:03Z warn: slow"},
		session.Line{Pipe: session.Stdout, Text: "1 2026-01-05T12:00:02Z resized"},
	)}
	read := tailOn(machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r1", "media", "shop-media-r1"),
	}})
	read.settle(t, 3, 0)
	if err := read.stop(t); err != nil {
		t.Errorf("Read() of a cancelled tail error = %v, want none", err)
	}

	want := []provider.LogEntry{
		{Time: time.Date(2026, 1, 5, 12, 0, 1, 0, time.UTC), App: "web", Release: "r1", Source: "http", Stream: provider.LogStreamStdout, Message: "served /"},
		{Time: time.Date(2026, 1, 5, 12, 0, 3, 0, time.UTC), App: "web", Release: "r1", Source: "http", Stream: provider.LogStreamStderr, Message: "warn: slow"},
		{Time: time.Date(2026, 1, 5, 12, 0, 2, 0, time.UTC), App: "web", Release: "r1", Source: "media", Stream: provider.LogStreamStdout, Message: "resized"},
	}
	if len(read.entries) != len(want) {
		t.Fatalf("Read() of a tail emitted %+v, want %+v", read.entries, want)
	}
	for i := range want {
		if read.entries[i] != want[i] {
			t.Errorf("Read() of a tail emitted %+v, want %+v", read.entries[i], want[i])
		}
	}

	var followers []string
	for _, command := range machine.commands() {
		if strings.Contains(command, "docker logs") {
			followers = append(followers, command)
		}
	}
	if len(followers) != 1 {
		t.Fatalf("Read() of a tail ran %d commands that follow logs, want one over one ssh session for every container", len(followers))
	}
	if names := followedNames(followers[0]); len(names) != 2 || names[0] != "shop-web-r1" || names[1] != "shop-media-r1" {
		t.Errorf("Read() of a tail followed %v, want each container in the order of its targets", names)
	}
	if !strings.Contains(followers[0], "'2026-01-05T11:00:00Z'") {
		t.Errorf("Read() of a tail ran %q, want it to follow from since", followers[0])
	}
}

func TestLogsTailKeepsOnlyTheLinesThatContainTheText(t *testing.T) {
	t.Parallel()

	machine := &box{follows: saying(
		session.Line{Text: "0 2026-01-05T12:00:01Z fine"},
		session.Line{Text: "0 2026-01-05T12:00:02Z boom one"},
	)}
	read := tailOn(machine, provider.LogQuery{Targets: []provider.LogTarget{tailTarget("r1", "http", "shop-web-r1")}, Contains: "boom"})
	read.settle(t, 1, 0)
	_ = read.stop(t)
	if len(read.entries) != 1 || read.entries[0].Message != "boom one" {
		t.Errorf("Read() of a tail emitted %+v, want only the line containing the text", read.entries)
	}
}

func TestLogsTailSaysAContainerIsGoneAndFollowsTheRest(t *testing.T) {
	t.Parallel()

	machine := &box{follows: saying(
		session.Line{Pipe: session.Stderr, Text: "0 Error response from daemon: No such container: shop-web-r1"},
		session.Line{Text: "1 2026-01-05T12:00:01Z still here"},
	)}
	read := tailOn(machine, provider.LogQuery{Targets: []provider.LogTarget{
		tailTarget("r1", "http", "shop-web-r1"),
		tailTarget("r2", "http", "shop-web-r2"),
	}})
	read.settle(t, 1, 1)
	if err := read.stop(t); err != nil {
		t.Fatalf("Read() of a tail error = %v, want a missing container reported and not refused", err)
	}
	if notice := read.notices[0]; notice.Kind != provider.LogSourceGone || notice.Target.Release != "r1" {
		t.Errorf("Read() of a tail said %+v, want the first release's source gone", notice)
	}
	if read.entries[0].Release != "r2" {
		t.Errorf("Read() of a tail emitted %+v, want the surviving release's line", read.entries)
	}
}

func TestLogsTailReturnsAFailureOfTheBox(t *testing.T) {
	t.Parallel()

	failure := errors.New("the connection dropped")
	machine := &box{follows: func(context.Context, string, func(session.Line) error) error { return failure }}
	read := tailOn(machine, provider.LogQuery{Targets: []provider.LogTarget{tailTarget("r1", "http", "shop-web-r1")}})
	select {
	case err := <-read.done:
		if !errors.Is(err, failure) {
			t.Errorf("Read() of a tail error = %v, want the box's failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read() of a tail whose box failed was still running after 5s")
	}
}

func TestLogsTailsNothingFromAnEmptyTargetList(t *testing.T) {
	t.Parallel()

	machine := &box{}
	read := tailOn(machine, provider.LogQuery{})
	if err := <-read.done; err != nil || len(read.entries) != 0 {
		t.Errorf("Read() of a tail with no targets = %+v, %v, want nothing", read.entries, err)
	}
	if len(machine.commands()) != 0 {
		t.Errorf("Read() of a tail with no targets ran %v, want no command", machine.commands())
	}
}

func TestLogsTailRefusesATargetThatIsNotAContainer(t *testing.T) {
	t.Parallel()

	read := tailOn(&box{}, provider.LogQuery{
		Targets: []provider.LogTarget{{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web", Physical: "web-fn"}}},
	})
	var refused refusal.Refusal
	if err := <-read.done; !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Read() of a tail error = %v, want an invalid refusal", err)
	}
}
