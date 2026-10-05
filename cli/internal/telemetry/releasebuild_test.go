package telemetry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	repoRoot        = "../../.."
	keySecret       = "PH_PROJECT_KEY"
	hostSecret      = "PH_API_HOST"
	telemetryPath   = "github.com/ocelhq/ocel/cli/internal/telemetry"
	releaseWorkflow = "binaries.yml"
)

func readRepoFile(t *testing.T, elems ...string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(append([]string{repoRoot}, elems...)...))
	if err != nil {
		t.Fatalf("read %v: %v", elems, err)
	}
	return string(content)
}

func TestTheReleaseConfigLinksTheKeyAndEndpointIntoTheCLIFromTheEnvironment(t *testing.T) {
	config := readRepoFile(t, ".goreleaser.yaml")

	for variable, secret := range map[string]string{"WriteKey": keySecret, "Endpoint": hostSecret} {
		want := "-X " + telemetryPath + "." + variable + `={{ envOrDefault "` + secret + `" "" }}`
		if !strings.Contains(config, want) {
			t.Errorf(".goreleaser.yaml lacks %q, want %s linked in and empty when the environment lacks it", want, variable)
		}
	}
}

func TestOnlyTheReleaseJobHoldsTheTelemetrySecrets(t *testing.T) {
	var files []string
	for _, pattern := range []string{".github/workflows/*", ".github/actions/*/*"} {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no workflow files found")
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		mentions := strings.Contains(string(content), keySecret) || strings.Contains(string(content), hostSecret)
		if mentions && filepath.Base(file) != releaseWorkflow {
			t.Errorf("%s names a telemetry secret, want only %s to", file, releaseWorkflow)
		}
	}

	workflow := readRepoFile(t, ".github/workflows", releaseWorkflow)
	_, step, found := strings.Cut(workflow, "- name: Release\n")
	if !found {
		t.Fatal("no Release step in " + releaseWorkflow)
	}
	if end := strings.Index(step, "\n  npm:"); end >= 0 {
		step = step[:end]
	}
	for _, secret := range []string{keySecret, hostSecret} {
		if !strings.Contains(step, "secrets."+secret) {
			t.Errorf("the release step lacks secrets.%s, want goreleaser to see it", secret)
		}
	}
}
