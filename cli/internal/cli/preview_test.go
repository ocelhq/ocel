package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/previewid"
)

// stubGit points the preview git/PR seams at fixed values for the duration of
// a test, restoring them afterward.
func stubGit(t *testing.T, branch, pr string) {
	t.Helper()
	prevBranch, prevPR := currentGitBranch, discoverPRNumber
	currentGitBranch = func(string) (string, error) { return branch, nil }
	discoverPRNumber = func() string { return pr }
	t.Cleanup(func() { currentGitBranch, discoverPRNumber = prevBranch, prevPR })
}

func TestRunPreviewUp_Ephemeral_SendsPreviewEphemeralEnvironment(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	want, err := previewid.Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("previewid.Resolve: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runPreviewUp(context.Background(), root, previewUpOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{
		"DEPLOY class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL",
		"identity=" + want.Key,
		"source=IDENTITY_SOURCE_GIT",
		"Preview " + want.Key + " is up",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("stdout = %q, want it to contain %q", out, sub)
		}
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunPreviewUp_PersistentNamed_SendsPersistentDeclaredEnvironment(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runPreviewUp(context.Background(), root, previewUpOptions{name: "staging"}, &stdout, &stderr); err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DEPLOY class=CLASS_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging source=IDENTITY_SOURCE_DECLARED") {
		t.Errorf("stdout = %q, want the persistent/declared Environment echo", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunPreviewUp_RefusesOnClassMismatch_NoDeploy(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runPreviewUp(context.Background(), root, previewUpOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runPreviewUp err = nil, want a class-mismatch error")
	}
	if !strings.Contains(err.Error(), "ocel preview can only run against preview infrastructure") {
		t.Errorf("err = %v, want the concrete class-mismatch message", err)
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

func TestRunPreviewUp_RefusesWhenInfraAbsent_NoDeploy(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "0")

	var stdout, stderr bytes.Buffer
	err := runPreviewUp(context.Background(), root, previewUpOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runPreviewUp err = nil, want a missing-infrastructure error")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap --preview") {
		t.Errorf("err = %v, want it to direct the user to `ocel bootstrap --preview`", err)
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

func TestRunPreviewRm_Ephemeral_DestroysCurrentBranchWithoutPrompting(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	want, err := previewid.Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("previewid.Resolve: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runPreviewRm(context.Background(), root, previewRmOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DESTROY project=proj_deploy_happy class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
		t.Errorf("stdout = %q, want the ephemeral Destroy echo for the current branch", out)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("stdout = %q, want no prompt for ephemeral teardown", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunPreviewRm_Ref_DestroysExplicitRef(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	// The current branch differs from --ref to prove --ref wins.
	stubGit(t, "some-other-branch", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	want, err := previewid.Resolve("release/v2", "")
	if err != nil {
		t.Fatalf("previewid.Resolve: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runPreviewRm(context.Background(), root, previewRmOptions{ref: "release/v2"}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "DESTROY project=proj_deploy_happy class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
		t.Errorf("stdout = %q, want the Destroy echo for the explicit ref", stdout.String())
	}
}

func TestResolveRmEnvironment_NameAndRefAreMutuallyExclusive(t *testing.T) {
	_, err := resolveRmEnvironment("", previewRmOptions{name: "staging", ref: "release/v2"})
	if err == nil {
		t.Fatal("resolveRmEnvironment(name+ref) err = nil, want a mutual-exclusion error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want it to explain --name and --ref are mutually exclusive", err)
	}
}

func TestRunPreviewRm_PersistentWithYes_DestroysWithoutPrompting(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runPreviewRm(context.Background(), root, previewRmOptions{name: "staging", yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DESTROY project=proj_deploy_happy class=CLASS_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging source=IDENTITY_SOURCE_DECLARED") {
		t.Errorf("stdout = %q, want the persistent Destroy echo", out)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("stdout = %q, want --yes to skip the prompt", out)
	}
}

// TestConfirmDestroyPreview covers the persistent-teardown prompt decision
// directly, mirroring TestConfirmDeploy: the interactive TTY-gated branch in
// runPreviewRm isn't exercised end to end (consistent with deploy/bootstrap).
func TestConfirmDestroyPreview(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"yes proceeds", "y\n", true},
		{"declined defaults to abort", "n\n", false},
		{"empty answer aborts", "\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got, err := confirmDestroyPreview("staging", &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDestroyPreview() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDestroyPreview(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), `Destroy persistent preview "staging"? [y/N]`) {
				t.Errorf("stdout = %q, want the persistent destroy prompt", stdout.String())
			}
		})
	}
}

func TestRunPreviewLs_RendersEnvironments(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runPreviewLs(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runPreviewLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{
		"feature_login_ab12cd34", "ephemeral", "pr-7",
		"staging", "persistent", "—",
		// The fake echoes the project_id the CLI scoped the listing to.
		"project:proj_deploy_happy",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("stdout = %q, want it to contain %q", out, sub)
		}
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunPreviewUp_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runPreviewUp(context.Background(), t.TempDir(), previewUpOptions{}, &bytes.Buffer{}, &stderr)

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runPreviewUp err = %v (%T), want *ExitError", err, err)
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunPreviewUp_NoProviderConfigured_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_no_provider",
};
`)

	err := runPreviewUp(context.Background(), root, previewUpOptions{name: "staging"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runPreviewUp err = nil, want error")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("err = %v, want it to mention the missing provider", err)
	}
}
