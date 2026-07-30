package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// setUpEnvFixture reuses the deploy fixture (project config, fake provider
// binary, preflighted production substrate) and points the fake provider at a
// store file that outlives the per-command provider process.
func setUpEnvFixture(t *testing.T) string {
	t.Helper()
	root, _ := setUpDeployFixture(t)
	t.Setenv(envFakeStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	return root
}

func envSet(t *testing.T, root, key, value string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvSet(context.Background(), root, key, value, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSet(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestRunEnvSet_ValueIsReadableBackAndRevealIsExplicit(t *testing.T) {
	root := setUpEnvFixture(t)

	if out := envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}); !strings.Contains(out, "STRIPE_API_KEY") {
		t.Errorf("set stdout = %q, want it to name the key it set", out)
	}

	var plain, revealed bytes.Buffer
	if err := runEnvGet(context.Background(), root, "STRIPE_API_KEY", envOptions{}, &plain, &plain); err != nil {
		t.Fatalf("runEnvGet err = %v; out=%s", err, plain.String())
	}
	if strings.Contains(plain.String(), "sk_live_secret") {
		t.Errorf("get stdout = %q, want the value withheld without --reveal", plain.String())
	}
	if !strings.Contains(plain.String(), "--reveal") {
		t.Errorf("get stdout = %q, want it to name the flag that reveals", plain.String())
	}

	if err := runEnvGet(context.Background(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &revealed, &revealed); err != nil {
		t.Fatalf("runEnvGet --reveal err = %v; out=%s", err, revealed.String())
	}
	if strings.TrimSpace(revealed.String()) != "sk_live_secret" {
		t.Errorf("get --reveal stdout = %q, want exactly the value so it is scriptable", revealed.String())
	}
}

func TestRunEnvLs_ShowsKeysAndMetadataButNeverValues(t *testing.T) {
	root := setUpEnvFixture(t)
	envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
	envSet(t, root, "POSTHOG_ID", "ph_public_id", envOptions{folder: "/web"})

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"STRIPE_API_KEY", "POSTHOG_ID", "/web"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls stdout = %q, want it to show %q", out, want)
		}
	}
	for _, secret := range []string{"sk_live_secret", "ph_public_id"} {
		if strings.Contains(out, secret) {
			t.Errorf("ls stdout = %q, want no value printed (found %q)", out, secret)
		}
	}
}

func TestRunEnvLs_ReportsAnEmptyStore(t *testing.T) {
	root := setUpEnvFixture(t)

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvLs err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ocel env set") {
		t.Errorf("ls on an empty store = %q, want it to name the command that fills it", stdout.String())
	}
}

func TestRunEnvRm_RemovesTheValue(t *testing.T) {
	root := setUpEnvFixture(t)
	envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

	var stdout, stderr bytes.Buffer
	if err := runEnvRm(context.Background(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRm err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
		t.Errorf("rm stdout = %q, want it to name the removed key", stdout.String())
	}

	var after bytes.Buffer
	if err := runEnvLs(context.Background(), root, envOptions{}, &after, &after); err != nil {
		t.Fatalf("runEnvLs err = %v", err)
	}
	if strings.Contains(after.String(), "STRIPE_API_KEY") {
		t.Errorf("ls after rm = %q, want the value gone", after.String())
	}
}

func TestRunEnvRm_ReportsNothingToRemove(t *testing.T) {
	root := setUpEnvFixture(t)

	var stdout, stderr bytes.Buffer
	if err := runEnvRm(context.Background(), root, "NEVER_SET", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRm err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No value") {
		t.Errorf("rm of an unset key = %q, want it to say there was nothing set", stdout.String())
	}
}

func TestRunEnvHistory_ReadsNewestFirstAndGatesTheValues(t *testing.T) {
	root := setUpEnvFixture(t)
	for _, v := range []string{"one", "two", "three"} {
		envSet(t, root, "STRIPE_API_KEY", v, envOptions{})
	}

	var plain bytes.Buffer
	if err := runEnvHistory(context.Background(), root, "STRIPE_API_KEY", envOptions{}, &plain, &plain); err != nil {
		t.Fatalf("runEnvHistory err = %v; out=%s", err, plain.String())
	}
	if strings.Contains(plain.String(), "three") {
		t.Errorf("history stdout = %q, want values withheld without --reveal", plain.String())
	}

	var revealed bytes.Buffer
	if err := runEnvHistory(context.Background(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &revealed, &revealed); err != nil {
		t.Fatalf("runEnvHistory --reveal err = %v; out=%s", err, revealed.String())
	}
	out := revealed.String()
	newest, oldest := strings.Index(out, "three"), strings.Index(out, "one")
	if newest < 0 || oldest < 0 {
		t.Fatalf("history --reveal stdout = %q, want every version's value", out)
	}
	if newest > oldest {
		t.Errorf("history --reveal stdout = %q, want newest first", out)
	}
}

func TestRunEnvGet_ReportsAnUnsetKey(t *testing.T) {
	root := setUpEnvFixture(t)

	var stdout, stderr bytes.Buffer
	err := runEnvGet(context.Background(), root, "NEVER_SET", envOptions{reveal: true}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvGet on an unset key err = nil, want a failure rather than an empty value")
	}
	if !strings.Contains(err.Error(), "NEVER_SET") {
		t.Errorf("runEnvGet err = %v, want it to name the key", err)
	}
}

// The two override axes are separate cells, not one another's fallback, so a
// folder value must not answer a root read.
func TestRunEnvGet_FolderAndRootAreSeparateCells(t *testing.T) {
	root := setUpEnvFixture(t)
	envSet(t, root, "POSTHOG_ID", "web-id", envOptions{folder: "/web"})

	var stdout, stderr bytes.Buffer
	if err := runEnvGet(context.Background(), root, "POSTHOG_ID", envOptions{reveal: true}, &stdout, &stderr); err == nil {
		t.Fatalf("runEnvGet at root err = nil (out=%q), want the root cell to be unset", stdout.String())
	}

	var folder bytes.Buffer
	if err := runEnvGet(context.Background(), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &folder, &folder); err != nil {
		t.Fatalf("runEnvGet in /web err = %v", err)
	}
	if strings.TrimSpace(folder.String()) != "web-id" {
		t.Errorf("get in /web = %q, want %q", folder.String(), "web-id")
	}
}

func TestRunEnvSet_RefusesOnPreviewInfrastructure(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet against preview infrastructure err = nil, want a class-mismatch refusal")
	}
}
