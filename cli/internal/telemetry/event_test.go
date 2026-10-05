package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

func TestTheAllowlistHoldsExactlyWhatTheEventsSend(t *testing.T) {
	completed, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}
	devEnded, err := NewDevSessionEnded(anIdentity, aTime, DevSession{})
	if err != nil {
		t.Fatal(err)
	}
	initCompleted, err := NewInitCompleted(anIdentity, aTime, InitCompletion{})
	if err != nil {
		t.Fatal(err)
	}

	sent := map[string]bool{}
	for _, event := range []Event{completed, devEnded, initCompleted} {
		for property := range event.Properties {
			sent[property] = true
		}
	}
	if got := slices.Sorted(maps.Keys(sent)); !slices.Equal(got, slices.Sorted(slices.Values(allowedProperties))) {
		t.Errorf("event properties = %v, want exactly the allowlist %v", got, allowedProperties)
	}
	names := []string{completed.Name, devEnded.Name, initCompleted.Name}
	if !slices.Equal(slices.Sorted(slices.Values(allowedEvents)), slices.Sorted(slices.Values(names))) {
		t.Errorf("allowed events = %v, want exactly %v", allowedEvents, names)
	}
}

func TestADevSessionEndedEventCarriesTheSessionAndTheBaseProperties(t *testing.T) {
	event, err := NewDevSessionEnded(anIdentity, aTime, DevSession{
		Duration:      90 * time.Second,
		Reloads:       3,
		ResourceKinds: []string{"bucket", "postgres"},
		ErrorCodes:    map[string]int{"dev.command_failed": 1, "env.missing": 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshal(t, event), &got); err != nil {
		t.Fatal(err)
	}
	if id, err := uuid.Parse(fmt.Sprint(got["uuid"])); err != nil || id.Version() != 7 {
		t.Errorf("uuid = %v, want a version 7 UUID: %v", got["uuid"], err)
	}
	delete(got, "uuid")
	want := map[string]any{
		"event":       "dev_session_ended",
		"distinct_id": anIdentity.InstallID,
		"timestamp":   "2026-10-05T12:30:45.123Z",
		"properties": map[string]any{
			"duration_ms":             float64(90000),
			"reloads":                 float64(3),
			"resource_kinds":          []any{"bucket", "postgres"},
			"error_codes":             map[string]any{"dev.command_failed": float64(1), "env.missing": float64(2)},
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

func TestADevSessionEndedEventSortsItsKindsAndSendsEmptyCollectionsNotNull(t *testing.T) {
	sorted, err := NewDevSessionEnded(anIdentity, aTime, DevSession{ResourceKinds: []string{"postgres", "bucket", "kv"}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := NewDevSessionEnded(anIdentity, aTime, DevSession{})
	if err != nil {
		t.Fatal(err)
	}

	if line := string(marshal(t, sorted)); !strings.Contains(line, `"resource_kinds":["bucket","kv","postgres"]`) {
		t.Errorf("event = %s, want the kinds sorted", line)
	}
	line := string(marshal(t, empty))
	for _, want := range []string{`"resource_kinds":[]`, `"error_codes":{}`, `"reloads":0`, `"duration_ms":0`} {
		if !strings.Contains(line, want) {
			t.Errorf("event = %s, want %s", line, want)
		}
	}
}

func TestAnInitCompletedEventCarriesTheLanguagePackageManagerProviderAndConfigFormat(t *testing.T) {
	event, err := NewInitCompleted(anIdentity, aTime, InitCompletion{Language: "node", PackageManager: "pnpm", Provider: "aws", ConfigFormat: "yaml"})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshal(t, event), &got); err != nil {
		t.Fatal(err)
	}
	if id, err := uuid.Parse(fmt.Sprint(got["uuid"])); err != nil || id.Version() != 7 {
		t.Errorf("uuid = %v, want a version 7 UUID: %v", got["uuid"], err)
	}
	delete(got, "uuid")
	want := map[string]any{
		"event":       "init_completed",
		"distinct_id": anIdentity.InstallID,
		"timestamp":   "2026-10-05T12:30:45.123Z",
		"properties": map[string]any{
			"language":                "node",
			"package_manager":         "pnpm",
			"provider":                "aws",
			"config_format":           "yaml",
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
	if id, err := uuid.Parse(fmt.Sprint(got["uuid"])); err != nil || id.Version() != 7 {
		t.Errorf("uuid = %v, want a version 7 UUID: %v", got["uuid"], err)
	}
	delete(got, "uuid")
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

func TestTwoEventsOfTheSameCompletionCarryDifferentUUIDs(t *testing.T) {
	first, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCommandCompleted(anIdentity, aTime, CommandCompletion{Command: "help"})
	if err != nil {
		t.Fatal(err)
	}

	if first.UUID == "" || first.UUID == second.UUID {
		t.Errorf("uuids = %q and %q, want two distinct ids so the server can deduplicate a resend", first.UUID, second.UUID)
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
