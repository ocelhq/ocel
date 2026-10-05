package telemetry_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

var anIdentity = telemetry.Identity{InstallID: "5f0c7c5e-6a7c-4a43-9d7e-0d1e5d0f6c11", CLIVersion: "1.2.3", OS: "linux", Arch: "amd64"}

func aCompletedEvent(t *testing.T, command string) telemetry.Event {
	t.Helper()
	event, err := telemetry.NewEvent(anIdentity, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), telemetry.CommandCompletion{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func commandsOf(t *testing.T, spool telemetry.Spool) []string {
	t.Helper()
	lines, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, line := range lines {
		var event telemetry.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, event.Properties["command"].(string))
	}
	return commands
}

func spoolFileSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}

func TestAnAppendedEventReadsBackInOrderOneJSONLinePerEvent(t *testing.T) {
	spool := telemetry.NewSpool(t.TempDir(), 1<<20)

	for _, command := range []string{"deploy", "env set", "help"} {
		if err := spool.Append(aCompletedEvent(t, command)); err != nil {
			t.Fatal(err)
		}
	}

	got := commandsOf(t, spool)
	if want := []string{"deploy", "env set", "help"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("spooled commands = %v, want %v", got, want)
	}
}

func TestASpoolNothingWasAppendedToIsEmptyAndReadingItCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	spool := telemetry.NewSpool(dir, 1<<20)

	lines, err := spool.Read()

	if err != nil || len(lines) != 0 || spool.HasEvents() {
		t.Errorf("Read() = %v, %v, HasEvents() = %v, want an empty spool", lines, err, spool.HasEvents())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want reading the spool to leave its directory uncreated", dir, err)
	}
}

func TestReadingASpoolWritesNoFileBesideTheEvents(t *testing.T) {
	dir := t.TempDir()
	spool := telemetry.NewSpool(dir, 1<<20)
	if err := spool.Append(aCompletedEvent(t, "deploy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "events.lock")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	before := filesIn(t, dir)

	if _, err := spool.Read(); err != nil {
		t.Fatal(err)
	}
	spool.HasEvents()

	if after := filesIn(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("files after reading = %v, want %v unchanged", after, before)
	}
}

func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestTheSpoolDropsTheOldestEventsWhenAnAppendWouldExceedItsCap(t *testing.T) {
	dir := t.TempDir()
	one, err := json.Marshal(aCompletedEvent(t, "cmd-00"))
	if err != nil {
		t.Fatal(err)
	}
	capBytes := int64(len(one)+1) * 3
	spool := telemetry.NewSpool(dir, capBytes)

	for i := range 10 {
		if err := spool.Append(aCompletedEvent(t, fmt.Sprintf("cmd-%02d", i))); err != nil {
			t.Fatal(err)
		}
		if size := spoolFileSize(t, dir); size > capBytes {
			t.Fatalf("after %d appends the spool is %d bytes, want at most %d", i+1, size, capBytes)
		}
	}

	got := commandsOf(t, spool)
	if want := []string{"cmd-07", "cmd-08", "cmd-09"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("spooled commands = %v, want only the newest %v", got, want)
	}
}

func TestAnEventLargerThanTheWholeSpoolIsRefused(t *testing.T) {
	spool := telemetry.NewSpool(t.TempDir(), 16)

	if err := spool.Append(aCompletedEvent(t, "deploy")); err == nil {
		t.Error("Append() = nil, want the oversized event refused")
	}
	if spool.HasEvents() {
		t.Error("the spool holds an event it should have refused")
	}
}

func TestConcurrentAppendsKeepEveryWholeLine(t *testing.T) {
	dir := t.TempDir()
	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spool := telemetry.NewSpool(dir, 1<<20)
			for i := range perWriter {
				if err := spool.Append(aCompletedEvent(t, fmt.Sprintf("w%d-%d", w, i))); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()

	if got := commandsOf(t, telemetry.NewSpool(dir, 1<<20)); len(got) != writers*perWriter {
		t.Errorf("spool holds %d events, want %d", len(got), writers*perWriter)
	}
}

func TestAnUnparseableLineInTheSpoolIsLeftOutOfWhatIsRead(t *testing.T) {
	dir := t.TempDir()
	spool := telemetry.NewSpool(dir, 1<<20)
	if err := spool.Append(aCompletedEvent(t, "deploy")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"event\":\"torn\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()

	got := commandsOf(t, spool)

	if fmt.Sprint(got) != "[deploy]" {
		t.Errorf("spooled commands = %v, want only the intact event", got)
	}
}
