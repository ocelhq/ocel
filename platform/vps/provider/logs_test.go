package vps_test

import (
	"context"
	"errors"
	"strings"
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
	})
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
	})

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
	})
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
	})
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
	}, func([]provider.LogEntry) error { return nil })

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Read() error = %v, want an invalid refusal", err)
	}
}
