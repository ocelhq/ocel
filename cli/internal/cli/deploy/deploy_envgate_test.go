package deploy

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

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func stubAppBuildRecorder(deps *cmddeps.Deps, built *bool) {
	deps.BuildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		*built = true
		return nil
	}
}

func captureBuildEnv(deps *cmddeps.Deps) *map[string]map[string]string {
	clitest.StubRecordedDeploymentIDs(deps)
	var got map[string]map[string]string
	deps.BuildApp = func(_ context.Context, _ *projectconfig.Config, envByApp map[string]map[string]string, _ io.Writer) error {
		got = envByApp
		return nil
	}
	return &got
}

func writeAppsConfig(t *testing.T, root, apps string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [`+apps+`],
};
`)
}

func TestEnvGateOnDeploy(t *testing.T) {
	t.Run("a missing value refuses before anything is built", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`)
		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the gate to refuse")
		}
		var exit *exitsig.ExitError
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
	})

	t.Run("a missing value refuses though discovery reported nothing", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t,
			`[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`,
			clitest.EnvDeclareOnlyScript)
		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the gate to refuse on what it knows itself")
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
	})

	t.Run("a value that is set passes the gate and deploys", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_value", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to have completed", stdout.String())
		}
	})

	t.Run("a value that cannot be read names the cell", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_value", envOptions{})
		t.Setenv(clitest.FakeRevealFailureEnvVar, "the store is unreachable")

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a store it cannot read to stop the deploy")
		}

		out := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(out, "STRIPE_API_KEY (project root)") {
			t.Errorf("output = %q, want it to name the cell that could not be read", out)
		}
	})

	t.Run("a live value is never handed to the declaring process", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		envSet(t, root, "LIVE_KEY", "sk_live_do_not_leak", envOptions{})
		envSet(t, root, "BAKED_KEY", "baked_value", envOptions{})

		cellsPath := filepath.Join(t.TempDir(), "cells.json")
		t.Setenv("OCEL_TEST_ENV_CELLS_OUT", cellsPath)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	})

	t.Run("a folder no app binds is a warning, not a refusal", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)
		writeAppsConfig(t, root, `{ name: "api", path: "apps/api", framework: "express" }`)
		writeAppSource(t, root, "api")
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		deps := clitest.NewDeps()
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v, want a dead scope to warn, not stop the deploy; stdout=%s", err, stdout.String())
		}
		out := stdout.String() + stderr.String()
		if !strings.Contains(out, "POSTHOG_ID") || !strings.Contains(out, "/web") {
			t.Errorf("output = %q, want a warning naming the key and the folder no app binds", out)
		}
	})

	t.Run("each app is built with its own diverged value", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)
		writeAppsConfig(t, root, `
    { name: "web", path: "apps/web", framework: "express", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "express", folder: "/admin" }`)
		writeAppSource(t, root, "web", "admin")
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		envSet(t, root, "POSTHOG_ID", "ph_admin", envOptions{folder: "/admin"})

		deps := clitest.NewDeps()
		got := captureBuildEnv(&deps)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		if (*got)["web"]["POSTHOG_ID"] != "ph_web" || (*got)["admin"]["POSTHOG_ID"] != "ph_admin" {
			t.Errorf("build environments = %v, want each app the value it resolved", *got)
		}
	})

	t.Run("a half-completed folder rename stops the deploy naming both files", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"],"source":"ocel/env.ts"}]`)
		writeAppsConfig(t, root, `
    { name: "web", path: "apps/web", framework: "express", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "express", folder: "/administration" }`)
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		envSet(t, root, "POSTHOG_ID", "ph_admin", envOptions{folder: "/admin"})

		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a half-finished folder rename to stop the deploy")
		}
		out := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{"POSTHOG_ID", "/admin", "ocel.config.ts", "env.ts"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want it to name %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the lint to refuse before any build runs")
		}
	})

	t.Run("a reference satisfies the gate with its source's value", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		ownedElsewhere(t, "POSTHOG_ID", "ph_owned_by_platform")
		envRef(t, root, "POSTHOG_ID", envOptions{}, envRefOptions{project: "platform"})

		deps := clitest.NewDeps()
		got := captureBuildEnv(&deps)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if (*got)[""]["POSTHOG_ID"] != "ph_owned_by_platform" {
			t.Errorf("build environment = %v, want the value the other project holds", *got)
		}
	})
}

func TestEnvGateOnPreviewUp(t *testing.T) {
	t.Run("a production value does not satisfy the preview gate", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want the preview gate to refuse: the production store is not the preview one")
		}

		out := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(out, "STRIPE_API_KEY") {
			t.Errorf("output = %q, want it to name the cell the preview bootstrap is missing", out)
		}
		if strings.Contains(out, "sk_live_secret") {
			t.Errorf("output = %q, want no production value reachable from a preview", out)
		}
		if built {
			t.Error("the app was built, want the gate to refuse before any build runs")
		}
	})

	t.Run("the environment being deployed resolves its own override", func(t *testing.T) {
		for name, tc := range map[string]struct {
			deploying string
			want      string
		}{
			"the environment holding the override": {deploying: "staging", want: "ph_staging"},
			"another preview":                      {deploying: "canary", want: "ph_shared"},
		} {
			t.Run(name, func(t *testing.T) {
				root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
				deps := clitest.NewDeps()
				stubGit(&deps, "feature/login", "")
				t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
				t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

				envSet(t, root, "POSTHOG_ID", "ph_shared", envOptions{preview: true})
				envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

				got := captureBuildEnv(&deps)

				var stdout, stderr bytes.Buffer
				if err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: tc.deploying}, &stdout, &stderr, strings.NewReader("")); err != nil {
					t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
				for app, env := range *got {
					if env["POSTHOG_ID"] != tc.want {
						t.Errorf("%s built with POSTHOG_ID=%q, want %q", app, env["POSTHOG_ID"], tc.want)
					}
				}
				if len(*got) == 0 {
					t.Fatal("no app was built, so nothing resolved a value")
				}
			})
		}
	})

	t.Run("an override is the only value its own environment needs", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		deps := clitest.NewDeps()
		stubGit(&deps, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

		got := captureBuildEnv(&deps)

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v, want staging's own override to satisfy the gate; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if len(*got) == 0 {
			t.Fatal("no app was built, so nothing resolved a value")
		}
		for app, env := range *got {
			if env["POSTHOG_ID"] != "ph_staging" {
				t.Errorf("%s built with POSTHOG_ID=%q, want %q", app, env["POSTHOG_ID"], "ph_staging")
			}
		}
	})

	t.Run("a redeployed branch finds the override it already had", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		deps := clitest.NewDeps()
		stubGit(&deps, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		envSet(t, root, "POSTHOG_ID", "ph_shared", envOptions{preview: true})
		envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

		got := captureBuildEnv(&deps)

		up := func(when string) {
			t.Helper()
			*got = nil
			var stdout, stderr bytes.Buffer
			if err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runPreviewUp %s err = %v; stdout=%s stderr=%s", when, err, stdout.String(), stderr.String())
			}
			if len(*got) == 0 {
				t.Fatalf("no app was built %s, so nothing resolved a value", when)
			}
			for app, env := range *got {
				if env["POSTHOG_ID"] != "ph_staging" {
					t.Errorf("%s built %s with POSTHOG_ID=%q, want %q", when, app, env["POSTHOG_ID"], "ph_staging")
				}
			}
		}

		up("before the teardown")

		var rm bytes.Buffer
		if err := runPreviewRm(context.Background(), deps, root, previewRmOptions{name: "staging", yes: true}, &rm, &rm, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; out=%s", err, rm.String())
		}

		up("after the branch was rebuilt")
	})
}
