package root

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

func withTelemetryEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	previous := telemetry.Endpoint
	telemetry.Endpoint = endpoint
	t.Cleanup(func() { telemetry.Endpoint = previous })
}

func aBatchServer(t *testing.T, status int) (url string, batches func() [][]json.RawMessage) {
	t.Helper()
	var mu sync.Mutex
	var received [][]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []json.RawMessage `json:"batch"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body %q is not JSON: %v", raw, err)
		}
		mu.Lock()
		received = append(received, body.Batch)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() [][]json.RawMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([][]json.RawMessage(nil), received...)
	}
}

func spooledCommands(t *testing.T) []string {
	t.Helper()
	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	events, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, raw := range events {
		var event telemetry.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, event.Properties["command"].(string))
	}
	return commands
}

func aSpooledEvent(t *testing.T, command string) {
	t.Helper()
	event, err := telemetry.NewCommandCompleted(telemetry.Identity{InstallID: "an-id"}, time.Now(), telemetry.CommandCompletion{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Append(event); err != nil {
		t.Fatal(err)
	}
}

func withBannerShown(t *testing.T) {
	t.Helper()
	telemetry.PrintBannerOnce(io.Discard, telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint))
}

func TestAnInvocationSpoolsItsCommandCompletedEventOutsideDebugMode(t *testing.T) {
	withTelemetryBuild(t)
	withBannerShown(t)

	executeAndReportRoot(t, "--help")

	if got := spooledCommands(t); len(got) != 1 || got[0] != "help" {
		t.Errorf("spooled commands = %v, want one event for help", got)
	}
}

func TestAnInvocationThatNeverShowsTheBannerSpoolsNothing(t *testing.T) {
	withTelemetryBuild(t)

	executeAndReportRoot(t, "--help")

	if got := spooledCommands(t); len(got) != 0 {
		t.Errorf("spooled commands = %v, want nothing recorded before the first-run banner is shown", got)
	}
}

func TestADebugInvocationSpoolsNothing(t *testing.T) {
	withTelemetryBuild(t)
	debugTelemetry(t)

	executeAndReportRoot(t, "--help")

	if got := spooledCommands(t); len(got) != 0 {
		t.Errorf("spooled commands = %v, want nothing in debug mode", got)
	}
}

func TestARecordedCommandStartsTheFlushOnlyAfterItsOutputIsWritten(t *testing.T) {
	withTelemetryBuild(t)
	ocel := newCommand()
	var out bytes.Buffer
	ocel.root.SetOut(&out)
	ocel.root.SetErr(io.Discard)
	var outputAtStart []int
	ocel.startFlush = func(telemetry.Resolution) { outputAtStart = append(outputAtStart, out.Len()) }

	ocel.executeAndReport([]string{"--help"})

	if len(outputAtStart) != 1 || outputAtStart[0] == 0 || outputAtStart[0] != out.Len() {
		t.Errorf("output length at flush start = %v, final = %d, want one start after all output", outputAtStart, out.Len())
	}
}

func TestTheFlushCommandAndShellCompletionStartNoFlush(t *testing.T) {
	withTelemetryBuild(t)
	for _, args := range [][]string{{"telemetry", "flush"}, {cobra.ShellCompRequestCmd, ""}} {
		ocel := newCommand()
		ocel.root.SetOut(io.Discard)
		ocel.root.SetErr(io.Discard)
		started := 0
		ocel.startFlush = func(telemetry.Resolution) { started++ }

		ocel.executeAndReport(args)

		if started != 0 {
			t.Errorf("ocel %s started the flush %d times, want none", strings.Join(args, " "), started)
		}
	}
}

func TestTheFlushCommandSendsTheSpoolAsOneBatchAndRecordsNoEventOfItsOwn(t *testing.T) {
	withTelemetryBuild(t)
	url, batches := aBatchServer(t, http.StatusOK)
	withTelemetryEndpoint(t, url)
	aSpooledEvent(t, "deploy")

	code, stdout, stderr := executeAndReportRoot(t, "telemetry", "flush")

	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("code = %d, stdout = %q, stderr = %q, want a silent success", code, stdout, stderr)
	}
	if got := batches(); len(got) != 1 || len(got[0]) != 1 || !strings.Contains(string(got[0][0]), `"command":"deploy"`) {
		t.Errorf("batches = %s, want one batch holding the spooled deploy event", got)
	}
	if got := spooledCommands(t); len(got) != 0 {
		t.Errorf("spooled commands = %v, want the sent event gone and no event for the flush itself", got)
	}
}

func TestTheFlushCommandKeepsTheSpoolAndRecordsNoEventWhenTheEndpointRefuses(t *testing.T) {
	withTelemetryBuild(t)
	url, batches := aBatchServer(t, http.StatusInternalServerError)
	withTelemetryEndpoint(t, url)
	aSpooledEvent(t, "deploy")

	code, stdout, stderr := executeAndReportRoot(t, "telemetry", "flush")

	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("code = %d, stdout = %q, stderr = %q, want a silent success", code, stdout, stderr)
	}
	if len(batches()) != 1 {
		t.Errorf("server saw %d batches, want one attempt and no retry", len(batches()))
	}
	if got := spooledCommands(t); len(got) != 1 || got[0] != "deploy" {
		t.Errorf("spooled commands = %v, want only the original deploy event", got)
	}
}

func TestTheFlushCommandExitsZeroAndPrintsNothingWhateverItIsGiven(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		args []string
	}{
		"no endpoint":      {args: nil},
		"unreachable":      {args: nil},
		"unknown flag":     {args: []string{"--no-such-flag"}},
		"extra argument":   {args: []string{"extra"}},
		"malformed json":   {env: map[string]string{"OCEL_JSON": "garbage"}},
		"malformed debug":  {env: map[string]string{"OCEL_DEBUG": "garbage"}},
		"machine output":   {args: []string{"--json"}},
		"verbose and conf": {args: []string{"-v", "--config", "/no/such/file"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			withTelemetryBuild(t)
			if name == "unreachable" {
				closed := httptest.NewServer(http.NotFoundHandler())
				closed.Close()
				withTelemetryEndpoint(t, closed.URL)
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			aSpooledEvent(t, "deploy")

			code, stdout, stderr := executeAndReportRoot(t, append([]string{"telemetry", "flush"}, tc.args...)...)

			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("code = %d, stdout = %q, stderr = %q, want exit 0 and silence", code, stdout, stderr)
			}
			if got := spooledCommands(t); len(got) != 1 {
				t.Errorf("spooled commands = %v, want the original event only", got)
			}
		})
	}
}

func TestTheFlushCommandNeitherPrintsNorMarksTheBanner(t *testing.T) {
	withTelemetryBuild(t)

	_, _, stderr := executeAndReportRoot(t, "telemetry", "flush")

	if stderr != "" || len(userconfig.Read()) != 0 {
		t.Errorf("stderr = %q, settings = %v, want the flush to leave the first-run banner for a real command", stderr, userconfig.Read())
	}
}

func TestTheFlushCommandIsHiddenFromHelpAndTheCommandCatalog(t *testing.T) {
	stdout, _ := executeRoot(t, "--help")
	catalog, _ := executeRoot(t, "help", "--json")

	if strings.Contains(stdout, "telemetry") || strings.Contains(catalog, "telemetry") {
		t.Errorf("help = %q, catalog = %q, want the hidden flush command named nowhere", stdout, catalog)
	}
}

func TestABareTelemetryCommandFailsAsAnUnknownCommandDoes(t *testing.T) {
	for _, prefix := range [][]string{nil, {"--json"}} {
		unknownCode, unknownOut, unknownErr := executeAndReportRoot(t, append(prefix, "no-such-command")...)

		code, stdout, stderr := executeAndReportRoot(t, append(prefix, "telemetry")...)

		wantOut := strings.ReplaceAll(unknownOut, "no-such-command", "telemetry")
		wantErr := strings.ReplaceAll(unknownErr, "no-such-command", "telemetry")
		if code != unknownCode || stdout != wantOut || stderr != wantErr {
			t.Errorf("ocel %v telemetry = %d, stdout %q, stderr %q, want %d, stdout %q, stderr %q", prefix, code, stdout, stderr, unknownCode, wantOut, wantErr)
		}
	}
}

func timeAnInvocation(args ...string) time.Duration {
	ocel := newCommand()
	ocel.root.SetOut(io.Discard)
	ocel.root.SetErr(io.Discard)
	started := time.Now()
	ocel.executeAndReport(args)
	return time.Since(started)
}

func TestACommandWithTelemetryOnTakesAtMostASmallMarginLongerThanWithItOff(t *testing.T) {
	const margin = 500 * time.Millisecond
	withTelemetryBuild(t)
	withBannerShown(t)
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(hanging.Close)
	t.Cleanup(func() { close(release) })
	withTelemetryEndpoint(t, hanging.URL)
	t.Setenv(rootArgsEnvVar, "telemetry flush")
	t.Setenv(telemetryKeyEnvVar, telemetry.WriteKey)
	t.Setenv(telemetryEndpointEnvVar, hanging.URL)

	t.Setenv("OCEL_TELEMETRY", "0")
	off := min(timeAnInvocation("--version"), timeAnInvocation("--version"))
	t.Setenv("OCEL_TELEMETRY", "")
	on := timeAnInvocation("--version")

	if on-off > margin {
		t.Errorf("with telemetry on the command took %s, off %s, want at most %s more", on, off, margin)
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the flush child never reached the endpoint, so the run with telemetry on started no real flush")
	}
}
