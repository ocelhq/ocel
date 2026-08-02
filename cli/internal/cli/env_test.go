package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
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

// A root value's FOLDER cell must not print a spelling `ocel env set --folder`
// rejects, so a value copied straight out of `ls` can be copied straight back
// in.
func TestRenderValues_RootFolderDoesNotPrintARejectedSlash(t *testing.T) {
	var stdout bytes.Buffer
	renderValues(&stdout, []*envv1.ValueMetadata{
		{Coordinate: &envv1.Coordinate{Key: "STRIPE_API_KEY", Folder: ""}},
	})

	out := stdout.String()
	if !strings.Contains(out, "(project root)") {
		t.Errorf("ls stdout = %q, want the root cell rendered as %q", out, "(project root)")
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			if f == "/" {
				t.Errorf("ls stdout = %q, want no field spelled %q: --folder / is rejected", out, "/")
			}
		}
	}
}

// The ENVIRONMENT column is the surviving half of an axis nothing writes any
// more. Printing a name an operator has no command for is only informative if
// `ls` says so, and saying so on every listing would name an axis most stores
// have nothing in.
func TestRenderValues_ExplainsANamedEnvironmentOnlyWhenOneIsShown(t *testing.T) {
	const note = "No command reaches those today"

	var withOverride bytes.Buffer
	renderValues(&withOverride, []*envv1.ValueMetadata{
		{Coordinate: &envv1.Coordinate{Key: "STRIPE_API_KEY"}},
		{Coordinate: &envv1.Coordinate{Key: "STRIPE_API_KEY", Environment: "pr-42-a1b2c3d4"}},
	})
	if out := withOverride.String(); !strings.Contains(out, note) {
		t.Errorf("ls stdout = %q, want it to explain that %q is unreachable", out, "pr-42-a1b2c3d4")
	}

	var classWideOnly bytes.Buffer
	renderValues(&classWideOnly, []*envv1.ValueMetadata{
		{Coordinate: &envv1.Coordinate{Key: "STRIPE_API_KEY"}},
	})
	if out := classWideOnly.String(); strings.Contains(out, note) {
		t.Errorf("ls stdout = %q, want no environment footnote when no row has one", out)
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

// History answers when a value changed and who it was, never what it was.
// Rotating a leaked key has to end the leak; a history that prints plaintext
// would keep the rotated-away value one command from a shared terminal for the
// next fifty writes.
func TestRunEnvHistory_ShowsMetadataNewestFirstAndNeverAPlaintext(t *testing.T) {
	root := setUpEnvFixture(t)
	secrets := []string{"sk_first", "sk_second", "sk_third"}
	for _, v := range secrets {
		envSet(t, root, "STRIPE_API_KEY", v, envOptions{})
	}

	// reveal is `ocel env get`'s flag. History must ignore it rather than
	// honour it, so the whole path is proven and not just the registration.
	for _, opts := range []envOptions{{}, {reveal: true}} {
		var stdout bytes.Buffer
		if err := runEnvHistory(context.Background(), root, "STRIPE_API_KEY", opts, &stdout, &stdout); err != nil {
			t.Fatalf("runEnvHistory(reveal=%v) err = %v; out=%s", opts.reveal, err, stdout.String())
		}
		out := stdout.String()

		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("history(reveal=%v) stdout = %q, want no plaintext (found %q)", opts.reveal, out, secret)
			}
		}
		if strings.Contains(out, "VALUE") {
			t.Errorf("history(reveal=%v) stdout = %q, want no VALUE column", opts.reveal, out)
		}

		rows := strings.Split(strings.TrimSpace(out), "\n")
		if len(rows) != 4 {
			t.Fatalf("history(reveal=%v) stdout = %q, want a header and three versions", opts.reveal, out)
		}
		for i, wantVersion := range []string{"3", "2", "1"} {
			if got := strings.Fields(rows[i+1])[0]; got != wantVersion {
				t.Errorf("history(reveal=%v) row %d = %q, want version %s: newest first", opts.reveal, i, rows[i+1], wantVersion)
			}
		}
	}
}

// Reveal stays on `ocel env get`, which prints one value the operator asked
// for by name. It never belonged on history, where one keystroke would print
// every retained version at once.
func TestEnvHistory_OffersNoRevealFlagWhereGetStillDoes(t *testing.T) {
	if f := envHistoryCmd.Flags().Lookup("reveal"); f != nil {
		t.Errorf("`ocel env history` registers --reveal (%q); history is metadata only", f.Usage)
	}
	if envGetCmd.Flags().Lookup("reveal") == nil {
		t.Error("`ocel env get` lost --reveal; reading one named value back is the surface history's removal relies on")
	}
}

// No runtime path resolves a named-environment override, and a preview's real
// identity is `sanitize(ref)-<8 hex>` rather than anything an operator would
// type, so a write flag naming an environment could only ever report success
// for a cell no deploy will read.
func TestEnvCommands_OfferNoEnvironmentWriteFlag(t *testing.T) {
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd} {
		if f := c.Flags().Lookup("environment"); f != nil {
			t.Errorf("`ocel env %s` registers --environment (%q); nothing resolves an override, so the write would report a success no deploy honours", c.Name(), f.Usage)
		}
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

func TestRunEnvSet_RefusesARootValueForAScopedKey(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "POSTHOG_ID", "ph_root", envOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: nothing could ever read it")
	}
	for _, want := range []string{"POSTHOG_ID", "/web", "/admin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestRunEnvSet_RefusesAScopedKeyInAFolderItDoesNotName(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "POSTHOG_ID", "ph", envOptions{folder: "/admin"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet err = nil, want a folder outside the key's scope refused")
	}
	if !strings.Contains(err.Error(), "/admin") {
		t.Errorf("err = %v, want it to name the folder it refused", err)
	}
}

func TestRunEnvSet_AcceptsAScopedKeyInAFolderItNames(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

	if out := envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"}); !strings.Contains(out, "/web") {
		t.Errorf("set stdout = %q, want the folder it wrote named", out)
	}
}

func TestRunEnvSet_LeavesAnUnscopedKeyWritableAtRootAndInAFolder(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true}]`)

	envSet(t, root, "LOG_LEVEL", "info", envOptions{})
	envSet(t, root, "LOG_LEVEL", "debug", envOptions{folder: "/web"})
}

// envDeclaringScript is a declaring process whose definitions are written into
// the code, the way a real defineEnv call's are, rather than read from the
// environment the way envDeclarationScript's are. A key's folder scope is a
// property of the project's source, so changing it here means editing the file
// — which is what makes "the declarations changed" observable from outside.
//
// It also appends a line to OCEL_TEST_DISCOVERY_LOG per run, so how many times
// the pass actually ran is a fact about the project's own files rather than
// anything about the CLI's internals.
func envDeclaringScript(definitions string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

globalThis.__ocelRegister.push(
  (async () => {
    const log = process.env.OCEL_TEST_DISCOVERY_LOG;
    if (log) await (await import("node:fs/promises")).appendFile(log, "ran\n");

    const res = await fetch(new URL("/resources.v1.ResourceService/DeclareEnv", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ definitions: %s }),
    });
    if (!res.ok) throw new Error("DeclareEnv failed: " + res.status + " " + (await res.text()));
  })(),
);
export {};
`, definitions)
}

func setUpDeclaringFixture(t *testing.T, definitions string) (root, log string) {
	t.Helper()
	root = setUpEnvGateFixtureWith(t, "[]", envDeclaringScript(definitions))
	log = filepath.Join(t.TempDir(), "discovery.log")
	t.Setenv("OCEL_TEST_DISCOVERY_LOG", log)
	return root, log
}

func discoveryRuns(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "ran\n")
}

// A write has to know a key's folder scope, which only the project's code
// knows, but a scripted run of writes should pay for learning it once. The
// declarations cannot have changed while the code they are written in has not.
func TestRunEnvSet_SecondWriteReusesTheDeclarationsTheFirstOneLearned(t *testing.T) {
	root, log := setUpDeclaringFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

	envSet(t, root, "POSTHOG_ID", "ph_one", envOptions{folder: "/web"})
	envSet(t, root, "POSTHOG_ID", "ph_two", envOptions{folder: "/web"})

	if got := discoveryRuns(t, log); got != 1 {
		t.Errorf("discovery ran %d times over two writes, want 1: nothing the declarations come from changed between them", got)
	}

	var out bytes.Buffer
	if err := runEnvGet(context.Background(), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &out, &out); err != nil {
		t.Fatalf("runEnvGet err = %v; out=%s", err, out.String())
	}
	if strings.TrimSpace(out.String()) != "ph_two" {
		t.Errorf("value in /web = %q, want %q: the second write must land like the first", out.String(), "ph_two")
	}
}

// The other half: a cache that outlives the code it was read from would put a
// value in a cell nothing resolves, silently. Scoping a key that was unscoped
// is exactly that change, and the next write must be judged by the new scope.
func TestRunEnvSet_PicksUpAScopeTheCodeGainedSinceTheLastWrite(t *testing.T) {
	root, log := setUpDeclaringFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)

	envSet(t, root, "POSTHOG_ID", "ph_root", envOptions{})

	writeFile(t, filepath.Join(root, "ocel", "env.ts"),
		envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`))

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "POSTHOG_ID", "ph_root_again", envOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet err = nil, want the scope the code now declares to refuse a root write")
	}
	if !strings.Contains(err.Error(), "/web") {
		t.Errorf("err = %v, want it to name the folder the key is now scoped to", err)
	}
	if got := discoveryRuns(t, log); got != 2 {
		t.Errorf("discovery ran %d times, want 2: the declaring code changed between the writes", got)
	}
}

// A declaration made conditional on ambient run-time state is missing from a
// set the last run produced, and no fingerprint over the code can see that it
// is missing. So a cached set may answer for a key it holds and never for one
// it does not: the alternative is writing a root cell for a scoped key,
// silently, which is the one thing the write guard exists to prevent.
func TestRunEnvSet_DoesNotTrustACachedAbsenceForAConditionallyScopedKey(t *testing.T) {
	root := setUpEnvGateFixtureWith(t, "[]", envDeclareOnlyScript)

	envSet(t, root, "LOG_LEVEL", "info", envOptions{})

	// Same bytes on disk, different ambient state: the declaring process now
	// scopes a key the cached set never mentioned.
	t.Setenv("OCEL_TEST_ENV_DEFINITIONS", `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "POSTHOG_ID", "ph_root", envOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: a cached set that never mentioned the key cannot say it is unscoped")
	}
	if !strings.Contains(err.Error(), "/web") {
		t.Errorf("err = %v, want it to name the folder the key is scoped to", err)
	}

	var out bytes.Buffer
	if err := runEnvGet(context.Background(), root, "POSTHOG_ID", envOptions{reveal: true}, &out, &out); err == nil {
		t.Errorf("runEnvGet at root err = nil (out=%q), want no root cell written", out.String())
	}
}
