package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// envDeclarationScript is a stand-in for what ocel/env's defineEnv does during
// discovery: declare every variable in one call, then report back the cells the
// answer shows it cannot run with. Writing it by hand rather than importing the
// SDK keeps this test about the CLI's half of the exchange — and lets it record
// exactly what plaintext the declaring process was handed.
const envDeclarationScript = `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

const call = async (method: string, body: unknown) => {
  const res = await fetch(new URL("/resources.v1.ResourceService/" + method, process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(method + " failed: " + res.status + " " + (await res.text()));
  return res.json();
};

globalThis.__ocelRegister.push(
  (async () => {
    const definitions = JSON.parse(process.env.OCEL_TEST_ENV_DEFINITIONS!);
    const { cells = [] } = await call("DeclareEnv", { definitions });

    const out = process.env.OCEL_TEST_ENV_CELLS_OUT;
    if (out) await (await import("node:fs/promises")).writeFile(out, JSON.stringify(cells));

    const problems = definitions
      .filter((d: any) => !cells.some((c: any) => c.key === d.key && !c.folder))
      .map((d: any) => ({ key: d.key, kind: "KIND_MISSING" }));
    if (problems.length > 0) await call("ReportEnvProblems", { problems });
  })(),
);
export {};
`

// setUpEnvGateFixture extends the deploy fixture with a definitions file
// declaring definitions, and points the fake provider at a store file the
// deploy and any `ocel env set` priming it share.
func setUpEnvGateFixture(t *testing.T, definitions string) string {
	t.Helper()
	root, _ := setUpDeployFixture(t)
	t.Setenv(envFakeStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	t.Setenv("OCEL_TEST_ENV_DEFINITIONS", definitions)
	writeFile(t, filepath.Join(root, "ocel", "env.ts"), envDeclarationScript)
	return root
}

// stubAppBuildRecorder records whether the app build ran, which is what
// "before anything is built" means from outside.
func stubAppBuildRecorder(t *testing.T, built *bool) {
	t.Helper()
	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, io.Writer) error {
		*built = true
		return nil
	}
	t.Cleanup(func() { buildApp = prev })
}

func TestRunDeploy_MissingValue_RefusesBeforeAnythingIsBuilt(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	built := false
	stubAppBuildRecorder(t, &built)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want the gate to refuse")
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Errorf("runDeploy err = %v, want a non-zero exit", err)
	}

	out := stdout.String()
	for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY <VALUE>"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if built {
		t.Error("the app was built, want the gate to refuse before any build runs")
	}
	if strings.Contains(out, "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", out)
	}
}

func TestRunDeploy_ValueSet_PassesTheGateAndDeploys(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	envSet(t, root, "STRIPE_API_KEY", "sk_live_value", envOptions{})

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want the deploy to have completed", stdout.String())
	}
}

func TestRunDeploy_LiveValueIsNeverHandedToTheDeclaringProcess(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
	envSet(t, root, "LIVE_KEY", "sk_live_do_not_leak", envOptions{})
	envSet(t, root, "BAKED_KEY", "baked_value", envOptions{})

	cellsPath := filepath.Join(t.TempDir(), "cells.json")
	t.Setenv("OCEL_TEST_ENV_CELLS_OUT", cellsPath)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	raw, readErr := os.ReadFile(cellsPath)
	if readErr != nil {
		t.Fatalf("read cells handed to discovery: %v", readErr)
	}
	if strings.Contains(string(raw), "sk_live_do_not_leak") {
		t.Errorf("cells = %s, want a live value never pulled onto the build host", raw)
	}

	var cells []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &cells); err != nil {
		t.Fatalf("unmarshal cells: %v", err)
	}
	byKey := map[string]string{}
	for _, c := range cells {
		byKey[c.Key] = c.Value
	}
	if _, ok := byKey["LIVE_KEY"]; !ok {
		t.Error("cells has no LIVE_KEY, want the live cell reported present so it is not called missing")
	}
	if byKey["BAKED_KEY"] != "baked_value" {
		t.Errorf("BAKED_KEY = %q, want the plaintext its schema is checked against", byKey["BAKED_KEY"])
	}
}
