package root

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func withTelemetryBuild(t *testing.T) {
	t.Helper()
	confighome.Isolate(t)
	previousKey, previousEndpoint := telemetry.WriteKey, telemetry.Endpoint
	telemetry.WriteKey, telemetry.Endpoint = "a-key", "http://127.0.0.1:1"
	t.Cleanup(func() { telemetry.WriteKey, telemetry.Endpoint = previousKey, previousEndpoint })
}

func TestTheTelemetryBannerPrintsOnStderrOnceAcrossInvocations(t *testing.T) {
	inADeployedProject(t)
	withTelemetryBuild(t)

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
	withTelemetryBuild(t)

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
	withTelemetryBuild(t)
	t.Setenv("OCEL_TELEMETRY", "0")

	_, stderr := executeRoot(t, "deployments", "prune", "--yes")

	if strings.Contains(stderr, "OCEL_TELEMETRY") {
		t.Errorf("stderr = %q, want no banner when opted out", stderr)
	}
}

func TestHelpNeverMentionsDoNotTrack(t *testing.T) {
	withTelemetryBuild(t)

	for _, path := range commandPaths(newCommand().root) {
		stdout, stderr := executeRoot(t, append(path, "--help")...)

		if strings.Contains(stdout+stderr, "DO_NOT_TRACK") {
			t.Errorf("ocel %s --help mentions DO_NOT_TRACK", strings.Join(path, " "))
		}
	}
}

func commandPaths(cmd *cobra.Command) [][]string {
	paths := [][]string{{}}
	for _, child := range cmd.Commands() {
		for _, path := range commandPaths(child) {
			paths = append(paths, append([]string{child.Name()}, path...))
		}
	}
	return paths
}

func TestShellCompletionLeavesTheTelemetryBannerForTheNextCommand(t *testing.T) {
	inADeployedProject(t)
	withTelemetryBuild(t)

	for _, completion := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		_, completionErr := executeRoot(t, completion, "deploy", "")
		if strings.Contains(completionErr, "OCEL_TELEMETRY") {
			t.Errorf("ocel %s stderr = %q, want no banner where the shell discards it", completion, completionErr)
		}
	}
	_, stderr := executeRoot(t, "deployments", "prune", "--yes")

	if !strings.Contains(stderr, "OCEL_TELEMETRY=0") {
		t.Errorf("stderr = %q, want the banner on the first command after shell completion", stderr)
	}
}
