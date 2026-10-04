package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var logsEpoch = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

func logsNow() time.Time { return logsEpoch.Add(10 * time.Minute) }

func buildIdentity(seq int) string {
	return fmt.Sprintf("%032x~%012x", seq+1, seq+1)
}

func recordApp(t *testing.T, project clitest.FakeProject, tier environment.Tier, env, app string, seq int, functionName, physical string) {
	t.Helper()
	build, err := provider.ParseBuild(buildIdentity(seq))
	if err != nil {
		t.Fatal(err)
	}
	err = stackrecords.Write(context.Background(), project.Provider.KeyValues(), tier, clitest.FixtureSlug, naming.AppStack(env, app, build.Release()), stackrecords.Stack{
		Kind:      provider.StackApp,
		App:       app,
		Release:   build.Release().String(),
		Build:     build.String(),
		Functions: []provider.Function{{Name: functionName, Physical: physical}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func line(offset time.Duration, message string) provider.LogEntry {
	return provider.LogEntry{Time: logsEpoch.Add(offset), Message: message, Instance: "i-1", Stream: provider.LogStreamStdout}
}

func liveEntry(message string) provider.LogEntry {
	return provider.LogEntry{Time: time.Now(), Message: message, Instance: "i-1", Stream: provider.LogStreamStdout}
}

func deployedWithLogs(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.RecordEdgeStack(t, project, environment.TierProduction, fake.KindRelay)
	recordApp(t, project, environment.TierProduction, stackrecords.ProductionEnv, "web", 0, "web-server", "web-old")
	recordApp(t, project, environment.TierProduction, stackrecords.ProductionEnv, "web", 1, "web-server", "web-live")
	recordApp(t, project, environment.TierProduction, stackrecords.ProductionEnv, "api", 2, "api-server", "api-live")
	promotion := router.Promotion{PromotionID: "promo-1", Ts: 1, Builds: map[string]string{"web": buildIdentity(1), "api": buildIdentity(2)}}
	if _, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).Promote(context.Background(), promotion, "", ""); err != nil {
		t.Fatal(err)
	}
	logs := project.Provider.FakeLogs()
	logs.Append("web-old", line(1*time.Minute, "old release"))
	logs.Append("web-live", line(2*time.Minute, "web is listening"))
	logs.Append("api-live", line(3*time.Minute, "api is listening"))
	return project
}

type logsRun struct {
	stdout, stderr string
	err            error
}

func dependenciesFor(stderr io.Writer, branch string) Dependencies {
	invocation := clitest.NewInvocation()
	clitest.AttachTerminalSink(invocation, stderr)
	return Dependencies{
		Invocation:       invocation,
		ReadGitBranch:    func(string) (string, error) { return branch, nil },
		DiscoverPRNumber: func() string { return "" },
		Now:              logsNow,
	}
}

func runWith(project clitest.FakeProject, dependencies func(io.Writer) Dependencies, opts logsOptions, apps ...string) logsRun {
	var stdout, stderr bytes.Buffer
	err := runLogs(context.Background(), dependencies(&stderr), project.Root, apps, opts, &stdout, &stderr)
	return logsRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func read(t *testing.T, project clitest.FakeProject, format terminal.Format, mutate func(*logsOptions), apps ...string) logsRun {
	t.Helper()
	opts := defaultLogsOptions()
	if mutate != nil {
		mutate(&opts)
	}
	got := runWith(project, func(stderr io.Writer) Dependencies {
		dependencies := dependenciesFor(stderr, "main")
		dependencies.Presentation = func(io.Writer) terminal.Presentation {
			return terminal.Resolve(terminal.Conditions{Format: format})
		}
		return dependencies
	}, opts, apps...)
	if got.err != nil {
		t.Fatalf("runLogs err = %v; stdout=%s stderr=%s", got.err, got.stdout, got.stderr)
	}
	return got
}

func linesOf(out string) []string {
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

func lastReadLogs(t *testing.T, project clitest.FakeProject) *contractv1.ReadLogsRequest {
	t.Helper()
	sent := clitest.RequestsTo[*contractv1.ReadLogsRequest](t, project.Requests, contractv1connect.ProviderServiceReadLogsProcedure)
	if len(sent) == 0 {
		t.Fatal("the provider received no ReadLogs request")
	}
	return sent[len(sent)-1]
}

func TestLogsPrintsEveryAppWhenNoneIsNamed(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatHuman, nil)

	want := []string{
		"2026-01-05T12:02:00.000Z web/http - web is listening",
		"2026-01-05T12:03:00.000Z api/http - api is listening",
	}
	if lines := linesOf(got.stdout); strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("stdout = %q, want the live release of every app, oldest first: %q", got.stdout, want)
	}
}

func TestLogsPrintsOnlyTheNamedApps(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatHuman, nil, "api")

	if want := "2026-01-05T12:03:00.000Z api/http - api is listening\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if apps := lastReadLogs(t, project).GetApps(); len(apps) != 1 || apps[0] != "api" {
		t.Errorf("the provider was asked for apps %v, want [api]", apps)
	}
}

func TestLogsJSONPrintsOneObjectPerLine(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatJSON, nil)

	lines := linesOf(got.stdout)
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want two entries and the caught-up notice, one per line", got.stdout)
	}
	entry := clitest.DecodeJSON(t, lines[0])
	want := map[string]any{
		"type":     "entry",
		"time":     "2026-01-05T12:02:00.000Z",
		"app":      "web",
		"source":   "http",
		"instance": "i-1",
		"stream":   "stdout",
		"message":  "web is listening",
		"failure":  false,
	}
	for key, value := range want {
		if entry[key] != value {
			t.Errorf("entry[%q] = %v, want %v in %s", key, entry[key], value, lines[0])
		}
	}
	if release, _ := entry["release"].(string); release == "" {
		t.Errorf("entry has no release in %s", lines[0])
	}
	if second := clitest.DecodeJSON(t, lines[1]); second["app"] != "api" {
		t.Errorf("second entry = %v, want api's", second)
	}
}

func TestLogsJSONKeepsTheRawMessageBesideTheParsedFields(t *testing.T) {
	project := deployedWithLogs(t)
	raw := `{"level":30,"msg":"listening","port":3000}`
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, raw))

	got := read(t, project, terminal.FormatJSON, nil, "web")

	var entry map[string]any
	for _, l := range linesOf(got.stdout) {
		if decoded := clitest.DecodeJSON(t, l); decoded["message"] == raw {
			entry = decoded
		}
	}
	if entry == nil {
		t.Fatalf("stdout = %q, want an entry whose message is the raw line %s", got.stdout, raw)
	}
	if entry["level"] != "info" {
		t.Errorf("level = %v, want info", entry["level"])
	}
	fields, _ := entry["fields"].(map[string]any)
	if fields["port"] != float64(3000) || len(fields) != 1 {
		t.Errorf("fields = %v, want only port 3000 beside the level and message", entry["fields"])
	}
}

func TestLogsJSONCarriesTheParsedErrorInTheFieldsAsPlainTextDoes(t *testing.T) {
	project := deployedWithLogs(t)
	raw := `{"level":50,"msg":"request failed","err":"boom","route":"/pay"}`
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, raw))

	plain := read(t, project, terminal.FormatHuman, nil, "web")
	asJSON := read(t, project, terminal.FormatJSON, nil, "web")

	if want := `ERROR request failed {"error":"boom","route":"/pay"}`; !strings.Contains(plain.stdout, want) {
		t.Errorf("stdout = %q, want %q", plain.stdout, want)
	}
	lines := linesOf(asJSON.stdout)
	fields, _ := clitest.DecodeJSON(t, lines[len(lines)-2])["fields"].(map[string]any)
	if fields["error"] != "boom" || fields["route"] != "/pay" || len(fields) != 2 {
		t.Errorf("fields = %v, want the error beside route, as plain text prints them", fields)
	}
}

func TestLogsJSONOmitsLevelAndFieldsForALineThatParsesToNeither(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatJSON, nil, "api")

	entry := clitest.DecodeJSON(t, linesOf(got.stdout)[0])
	for _, key := range []string{"level", "fields"} {
		if _, present := entry[key]; present {
			t.Errorf("entry has %q = %v, want it absent when nothing was parsed", key, entry[key])
		}
	}
}

func TestLogsDropsEntriesBelowTheRequestedLevel(t *testing.T) {
	project := deployedWithLogs(t)
	logs := project.Provider.FakeLogs()
	logs.Append("web-live",
		line(4*time.Minute, "DEBUG noisy detail"),
		line(5*time.Minute, "INFO all is well"),
		line(6*time.Minute, "WARN disk filling"),
		line(7*time.Minute, "ERROR it broke"),
		line(8*time.Minute, "a line with no level"),
	)

	got := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.level = "warn" }, "web")

	for _, shown := range []string{"disk filling", "it broke"} {
		if !strings.Contains(got.stdout, shown) {
			t.Errorf("stdout = %q, want it to keep %q", got.stdout, shown)
		}
	}
	for _, dropped := range []string{"noisy detail", "all is well", "no level", "web is listening"} {
		if strings.Contains(got.stdout, dropped) {
			t.Errorf("stdout = %q, want %q dropped: it is below warn or has no level", got.stdout, dropped)
		}
	}
}

func TestLogsPrintsAFailureAtErrorLevelSoLevelErrorKeepsIt(t *testing.T) {
	project := deployedWithLogs(t)
	failure := line(4*time.Minute, "timed out after 3000 ms")
	failure.Failure = true
	project.Provider.FakeLogs().Append("web-live", failure)

	plain := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.level = "error" }, "web")
	asJSON := read(t, project, terminal.FormatJSON, func(o *logsOptions) { o.level = "error" }, "web")

	if want := "2026-01-05T12:04:00.000Z web/http ERROR timed out after 3000 ms\n"; plain.stdout != want {
		t.Errorf("stdout = %q, want the failure alone, at error level: %q", plain.stdout, want)
	}
	entry := clitest.DecodeJSON(t, linesOf(asJSON.stdout)[0])
	if entry["level"] != "error" || entry["failure"] != true {
		t.Errorf("entry = %v, want level error and failure true", entry)
	}
}

func TestLogsTakesTheVendorSeverityWhenTheMessageNamesNoLevel(t *testing.T) {
	project := deployedWithLogs(t)
	severe := line(4*time.Minute, "disk filling")
	severe.Severity = "WARNING"
	overruled := line(5*time.Minute, "INFO all is well")
	overruled.Severity = "ERROR"
	project.Provider.FakeLogs().Append("web-live", severe, overruled)

	got := read(t, project, terminal.FormatHuman, nil, "web")

	for _, want := range []string{" web/http WARN disk filling\n", " web/http INFO all is well\n"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout = %q, want %q: the message's own level first, the vendor's severity when it has none", got.stdout, want)
		}
	}
}

func TestLogsRawFiltersByTheVendorSeverityAndFailureAlone(t *testing.T) {
	project := deployedWithLogs(t)
	severe := line(4*time.Minute, "INFO says the message")
	severe.Severity = "ERROR"
	failure := line(5*time.Minute, "ran out of memory (128 MB)")
	failure.Failure = true
	project.Provider.FakeLogs().Append("web-live", line(3*time.Minute, "WARN only the text says so"), severe, failure)

	got := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.raw, o.level = true, "warn" }, "web")

	want := "2026-01-05T12:04:00.000Z web/http ERROR INFO says the message\n" +
		"2026-01-05T12:05:00.000Z web/http ERROR ran out of memory (128 MB)\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want only what the provider marked as warn or worse: %q", got.stdout, want)
	}
}

func TestLogsStripsTerminalEscapesAndShowsLoneControlCharactersInMessages(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "\x1b[31mred\x1b[0m\x1b]0;owned\a\x1b[2J\u009b ok\r\x1b[1A"))

	got := read(t, project, terminal.FormatHuman, nil, "web")

	if strings.ContainsAny(got.stdout, "\x1b\a\r\u009b") {
		t.Errorf("stdout = %q, want no terminal control characters", got.stdout)
	}
	if !strings.Contains(got.stdout, ` - red\u009b ok`+"\n") {
		t.Errorf("stdout = %q, want the visible text kept and the lone control character shown as an escape", got.stdout)
	}
}

func TestLogsKeepsTheTextBeforeACarriageReturnSoALineCannotBeForged(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "GET /admin 403\rGET /admin 200"))

	got := read(t, project, terminal.FormatHuman, nil, "web")

	if !strings.Contains(got.stdout, ` - GET /admin 403\rGET /admin 200`+"\n") {
		t.Errorf("stdout = %q, want both halves of the line, the carriage return shown as an escape", got.stdout)
	}
}

func TestLogsStripsTerminalEscapesFromFieldValues(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "{\"level\":30,\"msg\":\"hit\",\"agent\":\"\u009b2Jevil\"}"))

	got := read(t, project, terminal.FormatHuman, nil, "web")

	if strings.ContainsAny(got.stdout, "\x1b\u009b") {
		t.Errorf("stdout = %q, want an attacker-controlled field value unable to send a control code", got.stdout)
	}
	if !strings.Contains(got.stdout, `{"agent":"\u009b2Jevil"}`) {
		t.Errorf("stdout = %q, want the fields as compact JSON after the message", got.stdout)
	}
}

func TestLogsJSONEscapesEveryControlCharacterOfTheRawMessage(t *testing.T) {
	project := deployedWithLogs(t)
	raw := "\x1b[31mred\x1b[0m \u009b2J \u202eexe.txt \x7f"
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, raw))

	got := read(t, project, terminal.FormatJSON, nil, "web")

	if strings.ContainsAny(got.stdout, "\x1b\u009b\u202e\x7f") {
		t.Errorf("stdout = %q, want the raw message JSON-escaped so piping it to a terminal is safe", got.stdout)
	}
	var found bool
	for _, l := range linesOf(got.stdout) {
		found = found || clitest.DecodeJSON(t, l)["message"] == raw
	}
	if !found {
		t.Errorf("stdout = %q, want the raw message intact once decoded", got.stdout)
	}
}

func TestLogsWritesItsOwnProgressToStderr(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatHuman, nil)

	if !strings.Contains(got.stderr, "test-app › production") {
		t.Errorf("stderr = %q, want the identity banner", got.stderr)
	}
	if strings.Contains(got.stdout, "test-app › production") || strings.Contains(got.stdout, "ocel") {
		t.Errorf("stdout = %q, want nothing but log lines", got.stdout)
	}
}

func TestLogsPrintsOneLineWithTimeAppSourceLevelMessageAndFields(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "level=error msg=\"db down\" retries=3"))

	got := read(t, project, terminal.FormatHuman, nil, "web")

	lines := linesOf(got.stdout)
	if want := `2026-01-05T12:04:00.000Z web/http ERROR db down {"retries":"3"}`; lines[len(lines)-1] != want {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], want)
	}
}

func TestLogsJoinsALogEntrysLinesWithAnEscapedNewlineAndKeepsTheirIndentation(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "boom\n  at run (app.js:1)\n"))

	got := read(t, project, terminal.FormatHuman, nil, "web")

	if !strings.Contains(got.stdout, `boom\n  at run (app.js:1)`+"\n") {
		t.Errorf("stdout = %q, want the entry on one line, its newlines escaped and its indentation kept", got.stdout)
	}
	if n := len(linesOf(got.stdout)); n != 2 {
		t.Errorf("stdout = %q has %d lines, want one per entry", got.stdout, n)
	}
}

func TestLogsPrintsOnlyTheLiveReleaseUnlessAllReleasesIsAsked(t *testing.T) {
	project := deployedWithLogs(t)

	live := read(t, project, terminal.FormatHuman, nil, "web")
	all := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.allReleases = true }, "web")

	if strings.Contains(live.stdout, "old release") {
		t.Errorf("stdout = %q, want the live release only", live.stdout)
	}
	if !strings.Contains(all.stdout, "old release") {
		t.Errorf("stdout = %q, want the older release too with --all-releases", all.stdout)
	}
	if !lastReadLogs(t, project).GetAllReleases() {
		t.Error("the provider was not told to read every release")
	}
}

func TestLogsSendsGrepToTheProviderAsContains(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.grep = "api is" })

	if want := "2026-01-05T12:03:00.000Z api/http - api is listening\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if contains := lastReadLogs(t, project).GetContains(); contains != "api is" {
		t.Errorf("contains = %q, want the grep text", contains)
	}
}

func TestLogsAsksForTheNewestLinesOfTheLastHourByDefault(t *testing.T) {
	project := deployedWithLogs(t)

	read(t, project, terminal.FormatHuman, nil)

	req := lastReadLogs(t, project)
	if req.GetLimit() != 100 {
		t.Errorf("limit = %d, want 100", req.GetLimit())
	}
	if got, want := req.GetSince().AsTime(), logsNow().Add(-time.Hour); !got.Equal(want) {
		t.Errorf("since = %s, want %s", got, want)
	}
	if req.GetUntil() != nil {
		t.Errorf("until = %v, want none", req.GetUntil())
	}
	if req.GetSlug() != clitest.FixtureSlug {
		t.Errorf("slug = %q, want %q", req.GetSlug(), clitest.FixtureSlug)
	}
}

func TestLogsReadsSinceAndUntilAsDurationsOrTimestamps(t *testing.T) {
	project := deployedWithLogs(t)

	cases := []struct {
		name         string
		since, until string
		wantSince    time.Time
		wantUntil    time.Time
	}{
		{"durations count back from now", "15m", "5m", logsNow().Add(-15 * time.Minute), logsNow().Add(-5 * time.Minute)},
		{"timestamps are taken as written", "2026-01-05T12:01:00Z", "2026-01-05T14:09:00+02:00", logsEpoch.Add(time.Minute), time.Date(2026, 1, 5, 12, 9, 0, 0, time.UTC)},
		{"a duration and a timestamp mix", "2h", "2026-01-05T12:05:00Z", logsNow().Add(-2 * time.Hour), logsEpoch.Add(5 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.since, o.until = tc.since, tc.until })

			req := lastReadLogs(t, project)
			if got := req.GetSince().AsTime(); !got.Equal(tc.wantSince) {
				t.Errorf("since = %s, want %s", got, tc.wantSince)
			}
			if got := req.GetUntil().AsTime(); !got.Equal(tc.wantUntil) {
				t.Errorf("until = %s, want %s", got, tc.wantUntil)
			}
		})
	}
}

func TestLogsRefusesOptionsThatCannotBeRead(t *testing.T) {
	project := deployedWithLogs(t)

	cases := []struct {
		name   string
		mutate func(*logsOptions)
		want   string
	}{
		{"a since that is neither", func(o *logsOptions) { o.since = "yesterday" }, "--since"},
		{"an until that is neither", func(o *logsOptions) { o.until = "soon" }, "--until"},
		{"a negative duration", func(o *logsOptions) { o.since = "-5m" }, "--since"},
		{"a window that ends before it starts", func(o *logsOptions) { o.since, o.until = "5m", "15m" }, "before"},
		{"an unknown level", func(o *logsOptions) { o.level = "loud" }, "--level"},
		{"no lines", func(o *logsOptions) { o.lines = 0 }, "--lines"},
		{"more lines than a read returns", func(o *logsOptions) { o.lines = 10001 }, "--lines"},
		{"an environment without preview", func(o *logsOptions) { o.environment = "staging" }, "--preview"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultLogsOptions()
			tc.mutate(&opts)
			got := runWith(project, func(stderr io.Writer) Dependencies { return dependenciesFor(stderr, "main") }, opts)
			if got.err == nil || !strings.Contains(got.err.Error(), tc.want) {
				t.Errorf("runLogs err = %v, want it to name %s", got.err, tc.want)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want nothing printed", got.stdout)
			}
		})
	}
}

func TestLogsRefusesAnAppThatIsNotDeployed(t *testing.T) {
	project := deployedWithLogs(t)

	got := runWith(project, func(stderr io.Writer) Dependencies { return dependenciesFor(stderr, "main") }, defaultLogsOptions(), "admin")

	if got.err == nil || !strings.Contains(got.stderr, "admin is not deployed here") {
		t.Errorf("runLogs err = %v, stderr = %q, want the provider's refusal naming admin on stderr", got.err, got.stderr)
	}
}

func previewDeployed(t *testing.T, env string) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
	recordApp(t, project, environment.TierPreview, env, "web", 0, "web-server", "web-preview")
	project.Provider.FakeLogs().Append("web-preview", line(1*time.Minute, "preview is listening"))
	return project
}

func TestLogsReadsTheCurrentBranchsPreviewWithPreview(t *testing.T) {
	id, err := previewid.Resolve("feature/login", "")
	if err != nil {
		t.Fatal(err)
	}
	project := previewDeployed(t, id.Key)
	opts := defaultLogsOptions()
	opts.preview, opts.allReleases = true, true

	got := runWith(project, func(stderr io.Writer) Dependencies { return dependenciesFor(stderr, "feature/login") }, opts)

	if got.err != nil {
		t.Fatalf("runLogs err = %v; stderr=%s", got.err, got.stderr)
	}
	if want := "2026-01-05T12:01:00.000Z web/http - preview is listening\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	env := lastReadLogs(t, project).GetEnvironment()
	if env.GetIdentity() != id.Key || env.GetLabel() != id.Label {
		t.Errorf("environment = %v, want identity %q and label %q as `ocel preview` resolves them", env, id.Key, id.Label)
	}
}

func TestLogsReadsTheNamedPreviewWithEnvironment(t *testing.T) {
	project := previewDeployed(t, "staging")
	opts := defaultLogsOptions()
	opts.preview, opts.environment, opts.allReleases = true, "staging", true

	got := runWith(project, func(stderr io.Writer) Dependencies {
		deps := dependenciesFor(stderr, "")
		deps.ReadGitBranch = func(string) (string, error) { return "", fmt.Errorf("the branch must not be read for a named preview") }
		return deps
	}, opts)

	if got.err != nil {
		t.Fatalf("runLogs err = %v; stderr=%s", got.err, got.stderr)
	}
	if !strings.Contains(got.stdout, "preview is listening") {
		t.Errorf("stdout = %q, want the named preview's logs", got.stdout)
	}
	if identity := lastReadLogs(t, project).GetEnvironment().GetIdentity(); identity != "staging" {
		t.Errorf("environment identity = %q, want staging", identity)
	}
}

func TestLogsRefusesAPreviewNameThatIsNotValid(t *testing.T) {
	project := deployedWithLogs(t)
	opts := defaultLogsOptions()
	opts.preview, opts.environment = true, "Not Valid"

	got := runWith(project, func(stderr io.Writer) Dependencies { return dependenciesFor(stderr, "main") }, opts)

	if got.err == nil || !strings.Contains(got.err.Error(), "invalid preview name") {
		t.Errorf("runLogs err = %v, want the preview name refused", got.err)
	}
}

func TestLogsRawPrintsMessagesAsSentWithoutParsing(t *testing.T) {
	project := deployedWithLogs(t)
	raw := `{"level":30,"msg":"listening","port":3000}`
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, raw))

	plain := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.raw = true }, "web")
	asJSON := read(t, project, terminal.FormatJSON, func(o *logsOptions) { o.raw = true }, "web")

	if want := "2026-01-05T12:04:00.000Z web/http - " + raw; !strings.Contains(plain.stdout, want) {
		t.Errorf("stdout = %q, want the line unparsed: %q", plain.stdout, want)
	}
	for _, l := range linesOf(asJSON.stdout) {
		entry := clitest.DecodeJSON(t, l)
		if entry["message"] != raw {
			continue
		}
		for _, key := range []string{"level", "fields"} {
			if _, present := entry[key]; present {
				t.Errorf("entry has %q = %v, want it absent when the message is not parsed", key, entry[key])
			}
		}
		return
	}
	t.Errorf("stdout = %q, want an entry carrying the raw message", asJSON.stdout)
}

func TestLogsJSONEndsHistoryWithACaughtUpNotice(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatJSON, nil)

	lines := linesOf(got.stdout)
	var notice struct {
		Type, Kind, Message string
		Omitted             *uint64
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &notice); err != nil {
		t.Fatalf("last line = %q: %v", lines[len(lines)-1], err)
	}
	if notice.Type != "notice" || notice.Kind != "caught_up" || notice.Omitted == nil || *notice.Omitted != 0 {
		t.Errorf("last line = %q, want a caught_up notice with its omitted count", lines[len(lines)-1])
	}
}

func TestLogsPlainTextKeepsTheCaughtUpNoticeOffStdout(t *testing.T) {
	project := deployedWithLogs(t)

	got := read(t, project, terminal.FormatHuman, nil)

	if strings.Contains(got.stdout, "caught") || len(linesOf(got.stdout)) != 2 {
		t.Errorf("stdout = %q, want log lines only", got.stdout)
	}
}

func TestLogsReportsAReleaseWhoseLogsAreGoneOnStderr(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Delete("web-old")

	got := read(t, project, terminal.FormatHuman, func(o *logsOptions) { o.allReleases = true }, "web")

	if !strings.Contains(got.stderr, "web (http) of release") || !strings.Contains(got.stderr, "no longer exists, so its logs are gone") {
		t.Errorf("stderr = %q, want the source-gone notice as a warning", got.stderr)
	}
	if strings.Contains(got.stdout, "no longer exists") {
		t.Errorf("stdout = %q, want log lines only", got.stdout)
	}
}

func TestLogsPlainTextReportsOtherNoticesAsWarningsNotLogData(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var warned []string
	out := newOutput(&stdout, terminal.Presentation{}, outputMode{}, newNotices(&stderr, terminal.Palette{}, func(message string) { warned = append(warned, message) }))

	for _, kind := range []contractv1.LogNotice_Kind{contractv1.LogNotice_KIND_CAUGHT_UP, contractv1.LogNotice_KIND_SOURCE_GONE} {
		notice := &contractv1.LogNotice{Kind: kind, Message: "web of release r1 no longer exists, so its logs are gone"}
		if err := out.printResponse(&contractv1.ReadLogsResponse{Body: &contractv1.ReadLogsResponse_Notice{Notice: notice}}); err != nil {
			t.Fatal(err)
		}
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: a notice is not log data", stdout.String())
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "no longer exists, so its logs are gone") {
		t.Errorf("warnings = %q, want the source-gone notice alone: caught up is no news in plain text", warned)
	}
}

func TestLogsJSONPrintsEveryKindOfNoticeAsAnObject(t *testing.T) {
	var stdout bytes.Buffer
	out := newOutput(&stdout, terminal.Presentation{}, outputMode{json: true}, newNotices(io.Discard, terminal.Palette{}, func(string) { t.Error("a JSON read warned instead of printing the notice") }))

	for _, kind := range []contractv1.LogNotice_Kind{contractv1.LogNotice_KIND_SAMPLED, contractv1.LogNotice_KIND_SOURCE_GONE, contractv1.LogNotice_KIND_RECONNECTED} {
		notice := &contractv1.LogNotice{Kind: kind, Message: "m", Omitted: 7}
		if err := out.printResponse(&contractv1.ReadLogsResponse{Body: &contractv1.ReadLogsResponse_Notice{Notice: notice}}); err != nil {
			t.Fatal(err)
		}
	}

	var kinds []string
	for _, l := range linesOf(stdout.String()) {
		notice := clitest.DecodeJSON(t, l)
		kinds = append(kinds, fmt.Sprint(notice["kind"]))
		if notice["type"] != "notice" || notice["message"] != "m" || notice["omitted"] != float64(7) {
			t.Errorf("notice = %v, want its type, message and omitted count", notice)
		}
	}
	if got := strings.Join(kinds, ","); got != "sampled,source_gone,reconnected" {
		t.Errorf("kinds = %s, want sampled,source_gone,reconnected", got)
	}
}

func TestLogsFlagsDefaultToTheLastHundredLinesOfTheLastHour(t *testing.T) {

	cmd := NewCommand(Dependencies{})

	for flag, want := range map[string]string{"lines": "100", "since": "1h", "until": "", "level": "", "grep": "", "raw": "false", "preview": "false", "environment": "", "all-releases": "false"} {
		found := cmd.Flags().Lookup(flag)
		if found == nil {
			t.Errorf("no --%s flag", flag)
			continue
		}
		if found.DefValue != want {
			t.Errorf("--%s defaults to %q, want %q", flag, found.DefValue, want)
		}
	}
	if cmd.Flags().ShorthandLookup("n") == nil || cmd.Flags().ShorthandLookup("n").Name != "lines" {
		t.Error("-n is not --lines")
	}
	if cmd.Use != "logs [app...]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "logs [app...]")
	}
}

func readOnTerminal(t *testing.T, project clitest.FakeProject, format terminal.Format, verbose bool, mutate func(*logsOptions)) logsRun {
	t.Helper()
	opts := defaultLogsOptions()
	if mutate != nil {
		mutate(&opts)
	}
	got := runWith(project, func(stderr io.Writer) Dependencies {
		dependencies := dependenciesFor(stderr, "main")
		dependencies.Presentation = func(io.Writer) terminal.Presentation {
			return terminal.Resolve(terminal.Conditions{Format: format, TTY: true, Verbose: verbose, ColorAsked: terminal.ColorNever})
		}
		return dependencies
	}, opts)
	if got.err != nil {
		t.Fatalf("runLogs err = %v; stdout=%s stderr=%s", got.err, got.stdout, got.stderr)
	}
	return got
}

func TestLogsOnATerminalPrintsTheTerminalViewOfEachEntry(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "level=warn msg=slow ms=900"))

	got := readOnTerminal(t, project, terminal.FormatHuman, false, nil)

	lines := linesOf(got.stdout)
	if want := "12:04:00.000  web/http  WARN   slow  ms=900"; lines[len(lines)-1] != want {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], want)
	}
	if want := "12:02:00.000  web/http  -      web is listening"; !strings.Contains(got.stdout, want+"\n") {
		t.Errorf("stdout = %q, want a line %q", got.stdout, want)
	}
}

func TestLogsOnATerminalBoxesAFailure(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", provider.LogEntry{Time: logsEpoch.Add(4 * time.Minute), Message: "Task timed out after 3.00 seconds", Failure: true, Stream: provider.LogStreamStdout})

	got := readOnTerminal(t, project, terminal.FormatHuman, false, nil)

	if !strings.Contains(got.stdout, "│ Task timed out after 3.00 seconds │") {
		t.Errorf("stdout = %q, want the failure in a box", got.stdout)
	}
}

func TestLogsJSONOnATerminalStillPrintsJSON(t *testing.T) {
	project := deployedWithLogs(t)

	got := readOnTerminal(t, project, terminal.FormatJSON, false, nil)

	if first := linesOf(got.stdout)[0]; !strings.HasPrefix(first, "{") {
		t.Errorf("first line = %q, want a JSON object", first)
	}
}

func TestLogsRawOnATerminalStillPrintsPlainLines(t *testing.T) {
	project := deployedWithLogs(t)

	got := readOnTerminal(t, project, terminal.FormatHuman, false, func(o *logsOptions) { o.raw = true })

	if want := "2026-01-05T12:02:00.000Z web/http - web is listening\n"; !strings.Contains(got.stdout, want) {
		t.Errorf("stdout = %q, want it to hold %q", got.stdout, want)
	}
}

func TestLogsOnATerminalShortensLongFieldValuesUnlessVerbose(t *testing.T) {
	project := deployedWithLogs(t)
	long := strings.Repeat("x", 100)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "level=info msg=big body="+long))

	compact := readOnTerminal(t, project, terminal.FormatHuman, false, nil)
	verbose := readOnTerminal(t, project, terminal.FormatHuman, true, nil)

	if strings.Contains(compact.stdout, long) || !strings.Contains(compact.stdout, "…") {
		t.Errorf("compact stdout = %q, want the value shortened", compact.stdout)
	}
	if !strings.Contains(verbose.stdout, long) {
		t.Errorf("verbose stdout = %q, want the full value", verbose.stdout)
	}
}

func TestLogsOnATerminalBoxesAStackTraceSentAsSeparateLines(t *testing.T) {
	project := deployedWithLogs(t)
	trace := []string{"Error: card declined", "    at charge (payments.js:12)", "    at handler (index.js:4)"}
	for i, message := range trace {
		entry := line(4*time.Minute+time.Duration(i)*time.Millisecond, message)
		entry.Severity = "ERROR"
		project.Provider.FakeLogs().Append("web-live", entry)
	}
	project.Provider.FakeLogs().Append("web-live", line(5*time.Minute, "recovered"))

	got := readOnTerminal(t, project, terminal.FormatHuman, false, nil)

	if strings.Count(got.stdout, "ERROR") != 1 {
		t.Errorf("stdout = %q, want the trace as one ERROR entry", got.stdout)
	}
	for _, frame := range trace {
		if !strings.Contains(got.stdout, "│ "+frame) {
			t.Errorf("stdout = %q, want %q inside the box", got.stdout, frame)
		}
	}
	if !strings.Contains(got.stdout, "  -      recovered\n") {
		t.Errorf("stdout = %q, want the next line as an entry of its own", got.stdout)
	}
}

func TestLogsJSONKeepsEachLineOfAStackTraceAsItsOwnObject(t *testing.T) {
	project := deployedWithLogs(t)
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute, "Error: card declined"))
	project.Provider.FakeLogs().Append("web-live", line(4*time.Minute+time.Millisecond, "    at charge (payments.js:12)"))

	got := readOnTerminal(t, project, terminal.FormatJSON, false, nil)

	if n := len(linesOf(got.stdout)); n != 5 {
		t.Errorf("stdout = %q, want 5 objects, one per provider entry", got.stdout)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type tailRun struct {
	t      *testing.T
	stdout *lockedBuffer
	stderr *lockedBuffer
	cancel context.CancelCauseFunc
	done   chan error
}

func plainDependencies(stderr io.Writer) Dependencies { return dependenciesFor(stderr, "main") }

func terminalDependencies(stderr io.Writer) Dependencies {
	dependencies := dependenciesFor(stderr, "main")
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{TTY: true, ColorAsked: terminal.ColorNever})
	}
	return dependencies
}

func jsonDependencies(stderr io.Writer) Dependencies {
	dependencies := dependenciesFor(stderr, "main")
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}
	return dependencies
}

func startTail(t *testing.T, project clitest.FakeProject, dependencies func(io.Writer) Dependencies, mutate func(*logsOptions)) *tailRun {
	t.Helper()
	opts := defaultLogsOptions()
	opts.tail = true
	if mutate != nil {
		mutate(&opts)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	run := &tailRun{t: t, stdout: &lockedBuffer{}, stderr: &lockedBuffer{}, cancel: cancel, done: make(chan error, 1)}
	t.Cleanup(func() { cancel(nil) })
	go func() {
		run.done <- runLogs(ctx, dependencies(run.stderr), project.Root, nil, opts, run.stdout, run.stderr)
	}()
	return run
}

func (r *tailRun) waitForTailOpened(project clitest.FakeProject) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := project.Provider.FakeLogs().WaitForTail(ctx); err != nil {
		r.t.Fatalf("the provider's tail was not opened: %v; stdout=%q stderr=%q", err, r.stdout.String(), r.stderr.String())
	}
}

func (r *tailRun) waitFor(out *lockedBuffer, want string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			r.t.Fatalf("output = %q, want it to hold %q; stderr=%q", out.String(), want, r.stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *tailRun) finish() error {
	r.t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		r.t.Fatalf("runLogs did not return; stdout=%q stderr=%q", r.stdout.String(), r.stderr.String())
		return nil
	}
}

func (r *tailRun) interrupt() error {
	r.cancel(nil)
	return r.finish()
}

func TestLogsTailPrintsHistoryThenLiveEntries(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, plainDependencies, nil)

	run.waitFor(run.stdout, "api is listening")
	run.waitForTailOpened(project)
	project.Provider.FakeLogs().Append("web-live", liveEntry("a live request"))
	run.waitFor(run.stdout, "a live request")

	if err := run.interrupt(); err != nil {
		t.Fatalf("runLogs err = %v", err)
	}
	lines := linesOf(run.stdout.String())
	if len(lines) != 3 ||
		lines[0] != "2026-01-05T12:02:00.000Z web/http - web is listening" ||
		lines[1] != "2026-01-05T12:03:00.000Z api/http - api is listening" ||
		!strings.HasSuffix(lines[2], "Z web/http - a live request") {
		t.Errorf("stdout = %q, want history oldest first and then the live entry", run.stdout.String())
	}
	if !lastReadLogs(t, project).GetTail() {
		t.Error("the provider was not asked to tail")
	}
}

func TestLogsAsksTheProviderToTailOnlyWithTail(t *testing.T) {
	project := deployedWithLogs(t)

	read(t, project, terminal.FormatHuman, nil)

	if lastReadLogs(t, project).GetTail() {
		t.Error("the provider was asked to tail without --tail")
	}
}

func TestLogsTailStopsAfterFor(t *testing.T) {
	project := deployedWithLogs(t)
	started := time.Now()
	run := startTail(t, project, plainDependencies, func(o *logsOptions) { o.stopAfter = "500ms" })

	err := run.finish()

	if err != nil {
		t.Fatalf("runLogs err = %v, want reaching --for to be a normal stop", err)
	}
	if took := time.Since(started); took < 500*time.Millisecond {
		t.Errorf("runLogs returned after %s, want it to tail until --for elapsed", took)
	}
}

func TestLogsTailExitsZeroWhenInterrupted(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, plainDependencies, nil)
	run.waitFor(run.stdout, "api is listening")
	run.waitForTailOpened(project)

	err := run.interrupt()

	if err != nil {
		t.Fatalf("runLogs err = %v, want an interrupted tail to end without an error", err)
	}
	if strings.Contains(run.stderr.String(), "cancelled") {
		t.Errorf("stderr = %q, want no cancellation notice for the normal way to stop", run.stderr.String())
	}
}

func TestLogsRefusesForWithoutTail(t *testing.T) {
	project := deployedWithLogs(t)
	opts := defaultLogsOptions()
	opts.stopAfter = "30s"

	got := runWith(project, plainDependencies, opts)

	if got.err == nil || got.err.Error() != "--for stops a --tail, so pass --tail with it" {
		t.Errorf("runLogs err = %v, want the one-line refusal naming --for and --tail", got.err)
	}
	if sent := clitest.RequestsTo[*contractv1.ReadLogsRequest](t, project.Requests, contractv1connect.ProviderServiceReadLogsProcedure); len(sent) != 0 {
		t.Errorf("the provider received %d ReadLogs requests, want none before the refusal", len(sent))
	}
}

func TestLogsRefusesATailOptionThatCannotBeRead(t *testing.T) {
	project := deployedWithLogs(t)

	cases := []struct {
		name   string
		mutate func(*logsOptions)
		want   string
	}{
		{"a for that is not a duration", func(o *logsOptions) { o.tail, o.stopAfter = true, "soon" }, "--for"},
		{"a for of zero", func(o *logsOptions) { o.tail, o.stopAfter = true, "0s" }, "--for"},
		{"a negative for", func(o *logsOptions) { o.tail, o.stopAfter = true, "-5s" }, "--for"},
		{"an until with tail", func(o *logsOptions) { o.tail, o.until = true, "1m" }, "--until"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultLogsOptions()
			tc.mutate(&opts)
			got := runWith(project, plainDependencies, opts)
			if got.err == nil || !strings.Contains(got.err.Error(), tc.want) {
				t.Errorf("runLogs err = %v, want it to name %s", got.err, tc.want)
			}
		})
	}
}

func TestLogsTailOnATerminalMarksWhereHistoryEndsAndLiveBegins(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, terminalDependencies, nil)

	run.waitFor(run.stdout, "── live ──")
	run.waitForTailOpened(project)
	project.Provider.FakeLogs().Append("web-live", liveEntry("a live request"))
	run.waitFor(run.stdout, "a live request")
	if err := run.interrupt(); err != nil {
		t.Fatalf("runLogs err = %v", err)
	}

	got := run.stdout.String()
	history, live, after := strings.Index(got, "api is listening"), strings.Index(got, "── live ──"), strings.Index(got, "a live request")
	if history >= live || live >= after || strings.Count(got, "── live ──") != 1 {
		t.Errorf("stdout = %q, want one live line between history and the live entry", got)
	}
}

func TestLogsOnATerminalPrintsNoLiveLineWithoutTail(t *testing.T) {
	project := deployedWithLogs(t)

	got := readOnTerminal(t, project, terminal.FormatHuman, false, nil)

	if strings.Contains(got.stdout, "live") {
		t.Errorf("stdout = %q, want no live line for a read that ends", got.stdout)
	}
}

func TestLogsTailWithRawOnATerminalPrintsNoLiveLineAsAPipeWouldNot(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, terminalDependencies, func(o *logsOptions) { o.raw = true })
	run.waitFor(run.stdout, "api is listening")
	run.waitForTailOpened(project)
	project.Provider.FakeLogs().Append("web-live", liveEntry("a live request"))
	run.waitFor(run.stdout, "a live request")
	if err := run.interrupt(); err != nil {
		t.Fatalf("runLogs err = %v", err)
	}

	if got := run.stdout.String(); strings.Contains(got, "live ──") || len(linesOf(got)) != 3 {
		t.Errorf("stdout = %q, want the log lines a pipe gets and no live line", got)
	}
}

func TestLogsTailReportsSamplingAndReconnectsOnStderrNotStdout(t *testing.T) {
	for name, dependencies := range map[string]func(io.Writer) Dependencies{
		"in plain text": plainDependencies,
		"on a terminal": terminalDependencies,
	} {
		t.Run(name, func(t *testing.T) {
			project := deployedWithLogs(t)
			run := startTail(t, project, dependencies, nil)
			run.waitFor(run.stdout, "api is listening")
			run.waitForTailOpened(project)

			project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogSampled, Omitted: 7})
			run.waitFor(run.stderr, sampledMessage)
			project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogReconnected})
			project.Provider.FakeLogs().Append("web-live", liveEntry("after the notices"))
			run.waitFor(run.stdout, "after the notices")
			if err := run.interrupt(); err != nil {
				t.Fatalf("runLogs err = %v", err)
			}

			notices := 0
			for _, l := range linesOf(run.stderr.String()) {
				if strings.Contains(l, "sampl") || strings.Contains(l, "reconnect") || strings.Contains(l, "omitted") {
					notices++
					if strings.Contains(l, "WARN") {
						t.Errorf("stderr line %q is a warning, want a muted line", l)
					}
				}
			}
			if notices != 2 {
				t.Errorf("stderr = %q, want one line for each of the two notices", run.stderr.String())
			}
			if out := run.stdout.String(); strings.Contains(out, "sampl") || strings.Contains(out, "reconnect") {
				t.Errorf("stdout = %q, want entries only", out)
			}
		})
	}
}

func TestLogsTailPrintsSamplingAndReconnectsAsNoticesInJSON(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, jsonDependencies, nil)
	run.waitFor(run.stdout, "caught_up")
	run.waitForTailOpened(project)

	project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogSampled, Omitted: 7})
	run.waitFor(run.stdout, `"kind":"sampled"`)
	project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogReconnected})
	run.waitFor(run.stdout, `"kind":"reconnected"`)
	if err := run.interrupt(); err != nil {
		t.Fatalf("runLogs err = %v", err)
	}

	if strings.Contains(run.stderr.String(), "sampl") || strings.Contains(run.stderr.String(), "reconnect") {
		t.Errorf("stderr = %q, want the notices only in the JSON stream", run.stderr.String())
	}
}

func TestLogsTailFlagsAreTailAndFor(t *testing.T) {
	cmd := NewCommand(Dependencies{})

	if tail := cmd.Flags().ShorthandLookup("t"); tail == nil || tail.Name != "tail" || tail.DefValue != "false" {
		t.Errorf("-t = %v, want --tail defaulting to false", tail)
	}
	if found := cmd.Flags().Lookup("for"); found == nil || found.DefValue != "" {
		t.Errorf("--for = %v, want a flag with no default", found)
	}
}

func TestLogsTailReportsAnErrorWhenStoppedForAnyReasonButAnInterruptOrFor(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, plainDependencies, nil)
	run.waitFor(run.stdout, "api is listening")
	run.waitForTailOpened(project)

	run.cancel(errors.New("the caller gave up on the read"))
	err := run.finish()

	if err == nil {
		t.Fatal("runLogs err = nil, want a tail stopped by anything but Ctrl-C or --for to fail")
	}
}

const sampledMessage = "7 entries were left out because the log store sampled them"

func TestLogsPlainTextPrintsSamplingAsAMutedLineOnStderrWithoutTail(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var warned []string
	out := newOutput(&stdout, terminal.Presentation{}, outputMode{}, newNotices(&stderr, terminal.Palette{}, func(message string) { warned = append(warned, message) }))

	notice := &contractv1.LogNotice{Kind: contractv1.LogNotice_KIND_SAMPLED, Message: sampledMessage, Omitted: 7}
	if err := out.printResponse(&contractv1.ReadLogsResponse{Body: &contractv1.ReadLogsResponse_Notice{Notice: notice}}); err != nil {
		t.Fatal(err)
	}

	if got := stderr.String(); got != sampledMessage+"\n" {
		t.Errorf("stderr = %q, want the sampling notice as one line", got)
	}
	if stdout.Len() != 0 || len(warned) != 0 {
		t.Errorf("stdout = %q, warnings = %q, want neither: sampling is a muted note", stdout.String(), warned)
	}
}

func colouredTerminalDependencies(stderr io.Writer) Dependencies {
	dependencies := dependenciesFor(stderr, "main")
	dependencies.Presentation = func(io.Writer) terminal.Presentation { return colouredTerminal() }
	return dependencies
}

func colouredTerminal() terminal.Presentation {
	return terminal.Resolve(terminal.Conditions{TTY: true, ColorAsked: terminal.ColorAlways})
}

func TestLogsTailOnAColouredTerminalMutesSamplingAndReconnectNotices(t *testing.T) {
	project := deployedWithLogs(t)
	run := startTail(t, project, colouredTerminalDependencies, nil)
	run.waitFor(run.stdout, "api is listening")
	run.waitForTailOpened(project)

	project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogSampled, Omitted: 7})
	project.Provider.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogReconnected})
	run.waitFor(run.stderr, "reconnected")
	if err := run.interrupt(); err != nil {
		t.Fatalf("runLogs err = %v", err)
	}

	palette := colouredTerminal().Palette()
	for _, message := range []string{sampledMessage, reconnectedMessage} {
		if !strings.Contains(run.stderr.String(), palette.Muted(message)) {
			t.Errorf("stderr = %q, want %q muted", run.stderr.String(), message)
		}
	}
	for _, l := range linesOf(run.stderr.String()) {
		if strings.Contains(l, "reconnected") && (strings.Contains(l, "INFO") || strings.Contains(l, "WARN")) {
			t.Errorf("stderr line %q carries a level label, want the muted message alone", l)
		}
	}
}

const reconnectedMessage = "the log stream dropped and reconnected, so entries written meanwhile may be missing"
