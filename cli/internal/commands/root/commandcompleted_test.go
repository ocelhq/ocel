package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
	"github.com/ocelhq/ocel/cli/internal/version"
)

func debugTelemetry(t *testing.T) {
	t.Helper()
	t.Setenv("OCEL_TELEMETRY", "debug")
}

func telemetryEvents(t *testing.T, stderr string) []telemetry.Event {
	t.Helper()
	var events []telemetry.Event
	for _, line := range strings.Split(stderr, "\n") {
		payload, ok := strings.CutPrefix(line, telemetry.DebugPrefix)
		if !ok {
			continue
		}
		var event telemetry.Event
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("line %q is not an event: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func theOnlyEvent(t *testing.T, stderr string) telemetry.Event {
	t.Helper()
	events := telemetryEvents(t, stderr)
	if len(events) != 1 {
		t.Fatalf("stderr = %q, want exactly one telemetry event, got %d", stderr, len(events))
	}
	return events[0]
}

func eventLine(t *testing.T, stderr string) string {
	t.Helper()
	_, line, found := strings.Cut(stderr, telemetry.DebugPrefix)
	if !found {
		t.Fatalf("stderr = %q, want a telemetry line", stderr)
	}
	return line
}

func flagsOf(t *testing.T, event telemetry.Event) []string {
	t.Helper()
	raw, ok := event.Properties["flags"].([]any)
	if !ok {
		t.Fatalf("flags = %#v, want a list", event.Properties["flags"])
	}
	names := make([]string, len(raw))
	for i, name := range raw {
		names[i] = name.(string)
	}
	return names
}

func TestACommandThatSucceedsRecordsOneCommandCompletedEvent(t *testing.T) {
	inADeployedProject(t)
	debugTelemetry(t)

	code, _, stderr := executeAndReportRoot(t, "deployments", "prune", "--yes")

	event := theOnlyEvent(t, stderr)
	if code != 0 || event.Name != "command_completed" {
		t.Fatalf("code = %d, event = %q, want 0 and command_completed", code, event.Name)
	}
	want := map[string]any{
		"command":                 "deployments prune",
		"exit_code":               float64(0),
		"error_code":              "",
		"json":                    false,
		"tty":                     false,
		"cli_version":             version.Version,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
		"$process_person_profile": false,
	}
	for name, value := range want {
		if event.Properties[name] != value {
			t.Errorf("property %s = %#v, want %#v", name, event.Properties[name], value)
		}
	}
	if got := flagsOf(t, event); !slices.Equal(got, []string{"yes"}) {
		t.Errorf("flags = %v, want [yes]", got)
	}
	if _, ok := event.Properties["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %#v, want a number", event.Properties["duration_ms"])
	}
}

func TestTheEventIsIdentifiedByTheInstallIDAndStampedInRFC3339(t *testing.T) {
	inADeployedProject(t)
	debugTelemetry(t)
	before := time.Now().Add(-time.Second)

	_, _, first := executeAndReportRoot(t, "deployments", "prune", "--yes")
	_, _, second := executeAndReportRoot(t, "deployments", "prune", "--yes")

	one, two := theOnlyEvent(t, first), theOnlyEvent(t, second)
	if one.DistinctID == "" || one.DistinctID != two.DistinctID {
		t.Errorf("distinct ids = %q and %q, want one stable install ID", one.DistinctID, two.DistinctID)
	}
	stamp, err := time.Parse(time.RFC3339, one.Timestamp)
	if err != nil || stamp.Before(before) || stamp.After(time.Now().Add(time.Second)) {
		t.Errorf("timestamp = %q (%v), want RFC 3339 and now", one.Timestamp, err)
	}
}

func TestBareOcelRecordsTheHelpCommand(t *testing.T) {
	debugTelemetry(t)

	code, _, stderr := executeAndReportRoot(t)

	event := theOnlyEvent(t, stderr)
	if code != 0 || event.Properties["command"] != "help" {
		t.Errorf("code = %d, command = %#v, want 0 and help", code, event.Properties["command"])
	}
	if got := flagsOf(t, event); len(got) != 0 {
		t.Errorf("flags = %v, want none", got)
	}
}

func TestANestedCommandRecordsItsWholePathWithoutTheBinaryName(t *testing.T) {
	debugTelemetry(t)

	_, _, stderr := executeAndReportRoot(t, "env", "set", "--help")

	if got := theOnlyEvent(t, stderr).Properties["command"]; got != "env set" {
		t.Errorf("command = %#v, want env set", got)
	}
}

func TestTheFlagsRecordedAreTheNamesTheUserSetSortedAndNeverTheirValues(t *testing.T) {
	debugTelemetry(t)
	secret := "sk-live-s3cr3t-/home/victor/acme-prod"

	_, stdout, stderr := executeAndReportRoot(t, "--verbose", "deployments", "prune", "--config", secret, "--json")

	event := theOnlyEvent(t, stderr)
	if got := flagsOf(t, event); !slices.Equal(got, []string{"config", "json", "verbose"}) {
		t.Errorf("flags = %v, want [config json verbose]", got)
	}
	line := eventLine(t, stderr)
	for _, leaked := range []string{"sk-live", "s3cr3t", "victor", "acme-prod", "/home"} {
		if strings.Contains(line, leaked) {
			t.Errorf("event %q carries %q from a flag value", line, leaked)
		}
	}
	if strings.Contains(stdout, telemetry.DebugPrefix) {
		t.Errorf("stdout = %q, want events on stderr only", stdout)
	}
}

func TestAnEnvVariableAssignmentNeverReachesTheEvent(t *testing.T) {
	inADeployedProject(t)
	debugTelemetry(t)

	_, _, stderr := executeAndReportRoot(t, "env", "set", "FOO=bar")

	line := eventLine(t, stderr)
	if strings.Contains(line, "FOO") || strings.Contains(line, "bar") {
		t.Errorf("event %q carries the variable name or value", line)
	}
}

func TestAFailingCommandRecordsItsCodedErrorCodeAndNoMessageText(t *testing.T) {
	debugTelemetry(t)
	t.Chdir(t.TempDir())

	code, _, stderr := executeAndReportRoot(t, "deployments", "prune", "--yes")

	event := theOnlyEvent(t, stderr)
	if code == 0 || event.Properties["exit_code"] != float64(code) {
		t.Fatalf("code = %d, exit_code = %#v, want the same non-zero code", code, event.Properties["exit_code"])
	}
	if event.Properties["error_code"] != "project.no_config" {
		t.Errorf("error_code = %#v, want project.no_config", event.Properties["error_code"])
	}
	message, _, _ := strings.Cut(stderr, telemetry.DebugPrefix)
	if strings.TrimSpace(message) == "" {
		t.Fatalf("stderr = %q, want the failure message before the event", stderr)
	}
	if strings.Contains(eventLine(t, stderr), "ocel.json") {
		t.Errorf("event %q carries message text", eventLine(t, stderr))
	}
}

func TestAnUncodedErrorRecordsInternalAndNoMessageText(t *testing.T) {
	debugTelemetry(t)
	ocel := newCommand()
	var errOut bytes.Buffer
	ocel.root.SetOut(&bytes.Buffer{})
	ocel.root.SetErr(&errOut)
	ocel.root.AddCommand(&cobra.Command{Use: "explode", RunE: func(*cobra.Command, []string) error {
		return errors.New("password hunter2 in /home/victor/acme")
	}})

	code := ocel.executeAndReport([]string{"explode"})

	event := theOnlyEvent(t, errOut.String())
	if code != 1 || event.Properties["error_code"] != "internal" || event.Properties["command"] != "explode" {
		t.Errorf("code = %d, event = %v, want exit 1 and error_code internal", code, event.Properties)
	}
	line := eventLine(t, errOut.String())
	if strings.Contains(line, "hunter2") || strings.Contains(line, "victor") {
		t.Errorf("event %q carries the error message", line)
	}
}

func TestAUsageErrorRecordsTheUsageCodeUnderJSONToo(t *testing.T) {
	debugTelemetry(t)

	for _, args := range [][]string{
		{"--json", "deploy", "--no-such-flag"},
		{"deploy", "--no-such-flag"},
		{"no-such-command"},
	} {
		code, _, stderr := executeAndReportRoot(t, args...)

		event := theOnlyEvent(t, stderr)
		if code != 1 || event.Properties["error_code"] != "usage" || event.Properties["exit_code"] != float64(1) {
			t.Errorf("ocel %s: code = %d, event = %v, want exit 1 and error_code usage", strings.Join(args, " "), code, event.Properties)
		}
		if strings.Contains(stderr, "no-such") && strings.Contains(eventLine(t, stderr), "no-such") {
			t.Errorf("ocel %s: event carries the mistyped name", strings.Join(args, " "))
		}
	}
}

func TestTheJSONPropertyFollowsMachineOutputSelection(t *testing.T) {
	debugTelemetry(t)

	_, _, byFlag := executeAndReportRoot(t, "--json")
	t.Setenv("OCEL_JSON", "1")
	_, _, byEnv := executeAndReportRoot(t)

	for name, stderr := range map[string]string{"flag": byFlag, "env": byEnv} {
		if got := theOnlyEvent(t, stderr).Properties["json"]; got != true {
			t.Errorf("json by %s = %#v, want true", name, got)
		}
	}
}

func TestTheTTYPropertyIsWhetherStdoutIsATerminal(t *testing.T) {
	debugTelemetry(t)
	tty, screen := aTerminal(t, "xterm", 80)
	var errOut bytes.Buffer
	ocel := newCommand()
	ocel.root.SetOut(tty)
	ocel.root.SetErr(&errOut)

	ocel.executeAndReport([]string{"--help"})
	screen()

	if got := theOnlyEvent(t, errOut.String()).Properties["tty"]; got != true {
		t.Errorf("tty = %#v, want true on a terminal", got)
	}
}

func TestAnInterruptedCommandRecordsExit130AndTheInterruptedCode(t *testing.T) {
	debugTelemetry(t)

	for name, interrupt := range map[string]error{
		"cancelled":                context.Canceled,
		"exit 130 already printed": &exitcode.ExitError{Code: exitcode.Interrupt},
	} {
		ocel := newCommand()
		var errOut bytes.Buffer
		ocel.root.SetOut(&bytes.Buffer{})
		ocel.root.SetErr(&errOut)
		ocel.root.AddCommand(&cobra.Command{Use: "interrupted", RunE: func(*cobra.Command, []string) error { return interrupt }})

		code := ocel.executeAndReport([]string{"interrupted"})

		event := theOnlyEvent(t, errOut.String())
		if code != 130 || event.Properties["exit_code"] != float64(130) || event.Properties["error_code"] != "interrupted" {
			t.Errorf("%s: code = %d, event = %v, want exit 130 and error_code interrupted", name, code, event.Properties)
		}
	}
}

func TestAnInterruptUnderJSONRecordsTheCodeItsErrorDocumentCarries(t *testing.T) {
	debugTelemetry(t)

	_, stdout, stderr := executeAndReportDataCommand(t, func() error { return context.Canceled })

	document := requireOneFailureDocument(t, stdout)
	if got := theOnlyEvent(t, stderr).Properties["error_code"]; got != "interrupted" || document["code"] != got {
		t.Errorf("error_code = %#v, document code = %#v, want both interrupted", got, document["code"])
	}
}

func TestACommandGroupRunWithoutASubcommandRecordsAUsageError(t *testing.T) {
	debugTelemetry(t)

	for _, group := range []string{"env", "bootstrap", "connector"} {
		code, _, stderr := executeAndReportRoot(t, group)

		event := theOnlyEvent(t, stderr)
		if code != 1 || event.Properties["error_code"] != "usage" {
			t.Errorf("ocel %s: code = %d, event = %v, want exit 1 and error_code usage", group, code, event.Properties)
		}
	}
}

func TestNothingIsPrintedWhenTelemetryIsDisabled(t *testing.T) {
	inADeployedProject(t)
	for name, set := range map[string]func(){
		"opted out":    func() { t.Setenv("OCEL_TELEMETRY", "0") },
		"false":        func() { t.Setenv("OCEL_TELEMETRY", "false") },
		"do not track": func() { t.Setenv("DO_NOT_TRACK", "1") },
		"no key":       func() {},
	} {
		t.Run(name, func(t *testing.T) {
			set()

			_, stdout, stderr := executeAndReportRoot(t, "deployments", "prune", "--yes")

			if strings.Contains(stdout+stderr, telemetry.DebugPrefix) {
				t.Errorf("stdout = %q, stderr = %q, want no event", stdout, stderr)
			}
		})
	}
}

func TestDisabledTelemetryNeverCreatesAnInstallID(t *testing.T) {
	confighome.Isolate(t)
	t.Setenv("OCEL_TELEMETRY", "0")

	executeAndReportRoot(t, "--help")

	if settings := userconfig.Read(); len(settings) != 0 {
		t.Errorf("settings = %v, want nothing stored while disabled", settings)
	}
}

func TestEnabledTelemetryWithoutDebugPrintsNothing(t *testing.T) {
	withTelemetryBuild(t)
	t.Setenv("OCEL_TELEMETRY", "")

	_, stdout, stderr := executeAndReportRoot(t, "--help")

	if strings.Contains(stdout+stderr, telemetry.DebugPrefix) {
		t.Errorf("stdout = %q, stderr = %q, want delivery to stay silent outside debug mode", stdout, stderr)
	}
}

func TestTheTelemetryFlushCommandRecordsNoEvent(t *testing.T) {
	debugTelemetry(t)
	ocel := newCommand()
	var errOut bytes.Buffer
	ocel.root.SetOut(&bytes.Buffer{})
	ocel.root.SetErr(&errOut)

	code := ocel.executeAndReport([]string{"telemetry", "flush"})

	if code != 0 || strings.Contains(errOut.String(), telemetry.DebugPrefix) {
		t.Errorf("code = %d, stderr = %q, want no event from the flush command", code, errOut.String())
	}
}

func TestShellCompletionRecordsNoEvent(t *testing.T) {
	debugTelemetry(t)

	for _, completion := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		_, stdout, stderr := executeAndReportRoot(t, completion, "env", "")

		if strings.Contains(stdout+stderr, telemetry.DebugPrefix) {
			t.Errorf("ocel %s: stdout = %q, stderr = %q, want no event from a tab press", completion, stdout, stderr)
		}
	}
}

func TestTheEventNamesTheDetectedAgentAndCIAndNothingWhenNoneIsDetected(t *testing.T) {
	debugTelemetry(t)
	for _, name := range []string{"AI_AGENT", "CLAUDECODE", "CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID", "GEMINI_CLI", "CURSOR_AGENT", "COPILOT_CLI", "COPILOT_AGENT", "OPENCODE", "CLINE_ACTIVE", "AGENT", "GITHUB_ACTIONS", "GITLAB_CI", "CIRCLECI", "BUILDKITE", "JENKINS_URL", "CI"} {
		t.Setenv(name, "")
	}

	_, _, none := executeAndReportRoot(t, "--help")
	t.Setenv("GEMINI_CLI", "1")
	t.Setenv("GITLAB_CI", "true")
	_, _, detected := executeAndReportRoot(t, "--help")

	if event := theOnlyEvent(t, none); event.Properties["agent"] != "" || event.Properties["ci"] != "" {
		t.Errorf("properties = %v, want empty agent and ci", event.Properties)
	}
	if event := theOnlyEvent(t, detected); event.Properties["agent"] != "gemini-cli" || event.Properties["ci"] != "gitlab" {
		t.Errorf("properties = %v, want gemini-cli and gitlab", event.Properties)
	}
}
