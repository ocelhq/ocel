package imagebuild_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
)

type builtPlan struct {
	Steps []struct {
		Name   string            `json:"name"`
		Assets map[string]string `json:"assets"`
	} `json:"steps"`
	Deploy struct {
		StartCommand string `json:"startCommand"`
	} `json:"deploy"`
}

func planned(t *testing.T, dir string) builtPlan {
	t.Helper()
	raw, err := imagebuild.Plan(dir)
	if err != nil {
		t.Fatalf("Plan(%s) = %v", dir, err)
	}
	var plan builtPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("the plan the frontend reads as railpack-plan.json is not JSON: %v", err)
	}
	return plan
}

func assets(t *testing.T, plan builtPlan) string {
	t.Helper()
	var b strings.Builder
	for _, step := range plan.Steps {
		for _, asset := range step.Assets {
			b.WriteString(asset)
		}
	}
	return b.String()
}

func TestAPlainServerIsPlannedWithNothingButItsOwnDirectory(t *testing.T) {
	plan := planned(t, "testdata/plainserver")

	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, want the start script the app already declares", plan.Deploy.StartCommand)
	}
	if !strings.Contains(assets(t, plan), "node = ") {
		t.Errorf("the plan installs no node toolchain, so railpack read a node app as something else:\n%s", assets(t, plan))
	}
}

func TestARailpackFileInTheAppChangesTheBuildOcelNeverReads(t *testing.T) {
	plan := planned(t, "testdata/configured")

	if want := "node server.js --from-railpack-json"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q from the app's own railpack.json", plan.Deploy.StartCommand, want)
	}
}

func TestThePlanCarriesNothingFromTheEnvironmentOcelRunsIn(t *testing.T) {
	t.Setenv("NODE_VERSION", "20")
	t.Setenv("RAILPACK_START_CMD", "node leaked.js")

	plan := planned(t, "testdata/plainserver")

	if strings.Contains(assets(t, plan), `node = "20`) {
		t.Errorf("a variable in ocel's own environment pinned the build's node version, so the build is not bare:\n%s", assets(t, plan))
	}
	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, which came from ocel's environment rather than the app", plan.Deploy.StartCommand)
	}
}

func TestNoVariableOcelRunsUnderAppearsAnywhereInThePlanItHandsTheFrontend(t *testing.T) {
	const leak = "a value the plan must never carry"
	t.Setenv("OCEL_PLAN_LEAK", leak)

	raw, err := imagebuild.Plan("testdata/plainserver")
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	for _, secret := range []string{"OCEL_PLAN_LEAK", leak} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the plan the frontend reads carries %q, so ocel's own environment reaches the build through the plan rather than through a build arg:\n%s", secret, raw)
		}
	}
}

func TestADirectoryRailpackCannotReadSaysWhyInsteadOfPlanningNothing(t *testing.T) {
	_, err := imagebuild.Plan(t.TempDir())
	if err == nil {
		t.Fatal("Plan() over an empty directory succeeded, so a build with nothing in it would be attempted")
	}
	if !strings.Contains(err.Error(), "railpack") {
		t.Errorf("Plan() over an empty directory = %v, and the reason never names the builder that refused", err)
	}
}
