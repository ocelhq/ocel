package logview_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ocelhq/ocel/cli/internal/logview"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

var viewEpoch = time.Date(2026, time.January, 5, 12, 4, 7, 123_000_000, time.UTC)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Errorf("output does not match %s.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func render(t *testing.T, verbose bool, lines ...logview.TerminalLine) string {
	t.Helper()
	var out bytes.Buffer
	return renderTo(t, &out, terminal.Detect(terminal.FormatHuman, verbose, &out), lines...)
}

func renderTo(t *testing.T, out *bytes.Buffer, present terminal.Presentation, lines ...logview.TerminalLine) string {
	t.Helper()
	view := logview.NewTerminalView(out, present)
	for _, line := range lines {
		if err := view.Write(line); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	return out.String()
}

func TestTerminalViewBoxesAnError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := render(t, false,
		logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{Message: "ready", Level: logview.LevelInfo}},
		logview.TerminalLine{Time: viewEpoch.Add(time.Second), App: "web", Entry: logview.Entry{
			Message: "db down",
			Level:   logview.LevelError,
			Fields:  map[string]any{"retries": "3", "host": "db.internal"},
			Error:   "Error: connect ECONNREFUSED\n    at connect (db.js:12)\n    at run (app.js:4)",
		}},
		logview.TerminalLine{Time: viewEpoch.Add(2 * time.Second), App: "web", Failure: true, Entry: logview.Entry{
			Message: "Task timed out after 3.00 seconds\n  at handler (index.js:9)",
			Level:   logview.LevelError,
		}},
	)
	golden(t, "TestTerminalViewBoxesAnError", got)
}

func TestTerminalViewShowsJSONFieldsAsKeyValues(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	flat := logview.Parse(`{"level":"info","msg":"order placed","orderId":"o-1","total":12.5,"paid":true,"note":"two words"}`)
	nested := logview.Parse(`{"level":"warn","msg":"slow query","db":{"host":"pg","pool":{"size":4}},"tags":["a","b"],"calls":[{"name":"x"}]}`)
	got := render(t, false,
		logview.TerminalLine{Time: viewEpoch, App: "api", Entry: flat},
		logview.TerminalLine{Time: viewEpoch, App: "api", Entry: nested},
	)
	golden(t, "TestTerminalViewShowsJSONFieldsAsKeyValues", got)
}

func TestTerminalViewShortensLongValuesUnlessVerbose(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	long := strings.Repeat("abcdefghij", 12)
	lines := []logview.TerminalLine{
		{Time: viewEpoch, App: "api", Entry: logview.Entry{Message: "inline", Level: logview.LevelInfo, Fields: map[string]any{"body": long, "id": "k"}}},
		{Time: viewEpoch, App: "api", Entry: logview.Entry{Message: "boxed", Level: logview.LevelError, Fields: map[string]any{"body": long}}},
	}
	golden(t, "TestTerminalViewShortensLongValuesUnlessVerbose.compact", render(t, false, lines...))
	golden(t, "TestTerminalViewShortensLongValuesUnlessVerbose.verbose", render(t, true, lines...))
}

func TestTerminalViewGivesEachAppTheSameColourEveryTime(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	apps := []string{"web", "api", "worker", "web", "cron", "api"}
	var lines []logview.TerminalLine
	for _, app := range apps {
		lines = append(lines, logview.TerminalLine{Time: viewEpoch, App: app, Entry: logview.Entry{Message: "tick", Level: logview.LevelInfo}})
	}
	first, second := render(t, false, lines...), render(t, false, lines...)
	if first != second {
		t.Errorf("two renders differ:\n%q\n%q", first, second)
	}
	golden(t, "TestTerminalViewGivesEachAppTheSameColourEveryTime", strings.ReplaceAll(first, "\x1b", "<ESC>"))
}

func TestTerminalViewWritesNoEscapesWithoutColour(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")
	got := render(t, false,
		logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{Message: "debugging", Level: logview.LevelDebug, Fields: map[string]any{"a": "b"}}},
		logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{Message: "careful", Level: logview.LevelWarn}},
		logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{Message: "broken", Level: logview.LevelError, Error: "trace"}},
	)
	if strings.Contains(got, "\x1b") {
		t.Errorf("output holds an escape code:\n%q", got)
	}
	golden(t, "TestTerminalViewWritesNoEscapesWithoutColour", got)
}

func TestTerminalViewFitsTheBoxToANarrowTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	present := terminal.Detect(terminal.FormatHuman, false, &out)
	present.Width, present.WidthMeasured = 40, true
	got := renderTo(t, &out, present,
		logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{
			Message: "payment provider refused the charge for order o-1",
			Level:   logview.LevelError,
			Error:   "Error: card_declined\n    at charge (payments/stripe-client.js:120:15)",
		}},
		logview.TerminalLine{Time: viewEpoch, App: "web", Failure: true, Entry: logview.Entry{Message: "short"}},
	)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Errorf("line %q takes %d columns, want at most 40", line, width)
		}
	}
	golden(t, "TestTerminalViewFitsTheBoxToANarrowTerminal", got)
}

func TestTerminalViewShowsTheTimeInTheLocalZone(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	utc := time.Local
	time.Local = time.FixedZone("UTC+2", 2*60*60)
	t.Cleanup(func() { time.Local = utc })
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{Message: "hello", Level: logview.LevelInfo}})
	if want := "14:04:07.123  web  INFO   hello\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTerminalViewQuotesInlineKeysAndValuesThatHoldSpacesOrEquals(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{
		Message: "saved",
		Level:   logview.LevelInfo,
		Fields: map[string]any{
			"my key": "v",
			"note":   "two words",
			"path":   `C:\my docs`,
			"query":  "a=b",
			"quote":  `say "hi"`,
			"tags":   []any{"a", "b"},
		},
	}})
	want := `12:04:07.123  web  INFO   saved  "my key"=v note="two words" path="C:\my docs" query="a=b" quote="say \"hi\"" tags=[a, b]` + "\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestTerminalViewShortensWideValuesByTheColumnsTheyTake(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	wide := strings.Repeat("漢", 60)
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{
		Message: "wide",
		Level:   logview.LevelInfo,
		Fields:  map[string]any{"name": wide},
	}})
	value := strings.TrimSuffix(got[strings.Index(got, "name=")+len("name="):], "\n")
	if width := ansi.StringWidth(value); width > 80 || !strings.HasSuffix(value, "…") {
		t.Errorf("value %q takes %d columns, want at most 80 ending in …", value, width)
	}
}

func TestTerminalViewNamesAnEntryWithoutAnAppWithADash(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, Entry: logview.Entry{Message: "hello", Level: logview.LevelInfo}})
	if want := "12:04:07.123  -  INFO   hello\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTerminalViewIndentsTheLaterLinesOfAMessage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{
		Message: "config loaded\nport: 3000\nhost: 0.0.0.0",
		Level:   logview.LevelInfo,
	}})
	want := "12:04:07.123  web  INFO   config loaded\n    port: 3000\n    host: 0.0.0.0\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTerminalViewBoxesTheMessageLinesAboveTheError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := render(t, false, logview.TerminalLine{Time: viewEpoch, App: "web", Entry: logview.Entry{
		Message: "request failed\nGET /orders",
		Level:   logview.LevelError,
		Error:   "Error: boom\n    at handler (index.js:9)",
	}})
	message, trace := strings.Index(got, "GET /orders"), strings.Index(got, "Error: boom")
	if message < 0 || trace < 0 || message > trace {
		t.Errorf("box = %q, want the message line GET /orders above the error", got)
	}
}
