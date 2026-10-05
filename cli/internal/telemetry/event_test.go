package telemetry

import (
	"bytes"
	"encoding/json"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/version"
)

var anIdentity = Identity{
	InstallID:  "5f0c7c5e-6a7c-4a43-9d7e-0d1e5d0f6c11",
	CLIVersion: "1.2.3",
	OS:         "linux",
	Arch:       "amd64",
	Agent:      "claude-code",
	CI:         "github-actions",
}

var aTime = time.Date(2026, 10, 5, 12, 30, 45, 123000000, time.UTC)

func TestAnEventNamedOutsideTheAllowlistIsRefused(t *testing.T) {
	_, err := newEvent("deploy_started", anIdentity, aTime, nil)

	if err == nil || !strings.Contains(err.Error(), "deploy_started") {
		t.Errorf("err = %v, want the event name refused", err)
	}
}

func TestAPropertyOutsideTheAllowlistIsRefused(t *testing.T) {
	for _, name := range []string{"email", "path", "project", "message", "flag_values", "distinct_id"} {
		_, err := newEvent("command_completed", anIdentity, aTime, map[string]any{name: "x"})

		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("property %q: err = %v, want it refused", name, err)
		}
	}
}

func TestTheAllowlistHoldsOnlyWhatACommandCompletedEventSends(t *testing.T) {
	event, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}

	if got := slices.Sorted(maps.Keys(event.Properties)); !slices.Equal(got, slices.Sorted(slices.Values(allowedProperties))) {
		t.Errorf("event properties = %v, want exactly the allowlist %v", got, allowedProperties)
	}
	if !slices.Equal(allowedEvents, []string{event.Name}) {
		t.Errorf("allowed events = %v, want only %q", allowedEvents, event.Name)
	}
}

func TestAnIdentityCarriesTheInstallIDTheBuildAndTheDetectedAgentAndCI(t *testing.T) {
	for _, name := range []string{"AI_AGENT", "CLAUDECODE", "CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID", "GITHUB_ACTIONS"} {
		t.Setenv(name, "")
	}
	t.Setenv("GEMINI_CLI", "1")
	t.Setenv("GITLAB_CI", "true")

	got := NewIdentity("an-install-id")

	want := Identity{InstallID: "an-install-id", CLIVersion: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Agent: "gemini-cli", CI: "gitlab"}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

func TestACommandCompletedEventCarriesTheCompletionAndTheBaseProperties(t *testing.T) {
	event, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{
		Command:   "env set",
		Flags:     []string{"config", "json"},
		ExitCode:  1,
		ErrorCode: "usage",
		Duration:  1500 * time.Millisecond,
		JSON:      true,
		TTY:       false,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshal(t, event), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"event":       "command_completed",
		"distinct_id": anIdentity.InstallID,
		"timestamp":   "2026-10-05T12:30:45.123Z",
		"properties": map[string]any{
			"command":                 "env set",
			"flags":                   []any{"config", "json"},
			"exit_code":               float64(1),
			"error_code":              "usage",
			"duration_ms":             float64(1500),
			"json":                    true,
			"tty":                     false,
			"cli_version":             "1.2.3",
			"os":                      "linux",
			"arch":                    "amd64",
			"agent":                   "claude-code",
			"ci":                      "github-actions",
			"$process_person_profile": false,
		},
	}
	if !bytes.Equal(marshal(t, got), marshal(t, want)) {
		t.Errorf("event = %v, want %v", got, want)
	}
}

func TestAnEventWithNoFlagsCarriesAnEmptyListAndNoAgentAnEmptyString(t *testing.T) {
	identity := anIdentity
	identity.Agent, identity.CI = "", ""

	event, err := NewCommandCompleted(identity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}

	line := string(marshal(t, event))
	for _, want := range []string{`"flags":[]`, `"agent":""`, `"ci":""`, `"error_code":""`} {
		if !strings.Contains(line, want) {
			t.Errorf("event = %s, want %s", line, want)
		}
	}
}

func TestTheTimestampIsRFC3339InUTC(t *testing.T) {
	local := time.Date(2026, 10, 5, 15, 30, 45, 0, time.FixedZone("EAT", 3*60*60))

	event, err := NewCommandCompleted(anIdentity, local, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := time.Parse(time.RFC3339, event.Timestamp)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC 3339: %v", event.Timestamp, err)
	}
	if !parsed.Equal(local) || !strings.HasSuffix(event.Timestamp, "Z") {
		t.Errorf("timestamp = %q, want the same instant in UTC", event.Timestamp)
	}
}

func TestSubmitPrintsOneJSONLineOnlyInDebugMode(t *testing.T) {
	event, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		resolution Resolution
		wantLine   bool
	}{
		"debug":    {Resolution{Enabled: true, Debug: true}, true},
		"enabled":  {Resolution{Enabled: true}, false},
		"disabled": {Resolution{}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			Submit(&out, tc.resolution, event)

			if !tc.wantLine {
				if out.Len() != 0 {
					t.Errorf("output = %q, want nothing", out.String())
				}
				return
			}
			rest, found := strings.CutPrefix(out.String(), DebugPrefix)
			if !found || strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(rest, "\n") {
				t.Fatalf("output = %q, want one [telemetry] line", out.String())
			}
			var decoded Event
			if err := json.Unmarshal([]byte(rest), &decoded); err != nil || decoded.Name != "command_completed" {
				t.Errorf("line = %q, want the event as JSON: %v", rest, err)
			}
		})
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
