package root

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func withTelemetryKey(t *testing.T) {
	t.Helper()
	confighome.Isolate(t)
	previous := telemetry.Key
	telemetry.Key = "a-key"
	t.Cleanup(func() { telemetry.Key = previous })
}

func TestTheTelemetryBannerPrintsOnStderrOnceAcrossInvocations(t *testing.T) {
	inADeployedProject(t)
	withTelemetryKey(t)

	firstOut, firstErr := executeRoot(t, "deployments", "prune", "--yes")
	secondOut, secondErr := executeRoot(t, "deployments", "prune", "--yes")

	if !strings.Contains(firstErr, "OCEL_TELEMETRY=0") {
		t.Errorf("first stderr = %q, want the telemetry banner", firstErr)
	}
	if strings.Contains(firstOut, "OCEL_TELEMETRY") {
		t.Errorf("first stdout = %q, want the banner on stderr only", firstOut)
	}
	if strings.Contains(secondErr, "OCEL_TELEMETRY") || strings.Contains(secondOut, "OCEL_TELEMETRY") {
		t.Errorf("second run printed the banner again: stdout %q, stderr %q", secondOut, secondErr)
	}
}

func TestTheTelemetryBannerLeavesJSONStdoutAlone(t *testing.T) {
	inADeployedProject(t)
	withTelemetryKey(t)

	stdout, stderr := executeRoot(t, "--json", "deployments", "prune", "--yes")

	if strings.Contains(stdout, "OCEL_TELEMETRY") {
		t.Errorf("stdout = %q, want the banner kept off the JSON stream", stdout)
	}
	if !strings.Contains(stderr, "OCEL_TELEMETRY=0") {
		t.Errorf("stderr = %q, want the banner there", stderr)
	}
}

func TestNoTelemetryBannerWhenTheBuildCarriesNoKey(t *testing.T) {
	inADeployedProject(t)

	_, stderr := executeRoot(t, "deployments", "prune", "--yes")

	if strings.Contains(stderr, "OCEL_TELEMETRY") {
		t.Errorf("stderr = %q, want no banner without a key", stderr)
	}
}

func TestNoTelemetryBannerWhenOptedOut(t *testing.T) {
	inADeployedProject(t)
	withTelemetryKey(t)
	t.Setenv("OCEL_TELEMETRY", "0")

	_, stderr := executeRoot(t, "deployments", "prune", "--yes")

	if strings.Contains(stderr, "OCEL_TELEMETRY") {
		t.Errorf("stderr = %q, want no banner when opted out", stderr)
	}
}

func TestHelpNeverMentionsDoNotTrack(t *testing.T) {
	withTelemetryKey(t)

	for _, args := range [][]string{{"--help"}, {"deploy", "--help"}, {"deployments", "--help"}} {
		stdout, stderr := executeRoot(t, args...)

		if strings.Contains(stdout+stderr, "DO_NOT_TRACK") {
			t.Errorf("ocel %s mentions DO_NOT_TRACK", strings.Join(args, " "))
		}
	}
}
