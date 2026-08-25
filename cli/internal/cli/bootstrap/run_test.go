package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRunBootstrapDestroy(t *testing.T) {
	t.Run("--yes skips the phrase and the terminal requirement", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "Type the environment name") {
			t.Errorf("stdout = %q, want --yes to skip the typed phrase", out)
		}
		if !strings.Contains(out, "TEARDOWN tier=TIER_PRODUCTION") {
			t.Errorf("stdout = %q, want the production teardown", out)
		}
	})

	t.Run("the bypass env skips the phrase and says so", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(changeplan.BypassEnv, "production")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Type the environment name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed phrase", stdout.String())
		}
		if !strings.Contains(stderr.String(), changeplan.BypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed teardown is never silent", stderr.String(), changeplan.BypassEnv)
		}
	})

	t.Run("a bypass naming the other bootstrap is refused, not ignored", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(changeplan.BypassEnv, "preview")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the mismatched-bypass refusal")
		}
		for _, want := range []string{changeplan.BypassEnv, "preview", "production"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("without a terminal, a phrase it cannot ask for is a refusal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		opts := Options{}
		err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the no-terminal refusal")
		}
		for _, want := range []string{"interactive terminal", "--yes", changeplan.BypassEnv} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestRunBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		t.Parallel()

		err := Run(context.Background(), clitest.NewDeps(), t.TempDir(), environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := Run(context.Background(), clitest.NewDeps(), root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
	})
}

func TestBootstrapShowsItsPlan(t *testing.T) {
	t.Run("it renders every group and the tally before applying", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Proposed changes to the production bootstrap",
			"~ stack ocel-production-core — content is behind this build",
			"    ~ OcelRouterFunction (AWS::Lambda::Function)",
			"    ± OcelOriginSecret (AWS::SecretsManager::Secret) — rotation forces replacement",
			"+ stack ocel-production-image-optimization — image-optimization joins the feature set",
			"– stack ocel-production-isr — isr leaves the set; web, api were deployed against it (this one is slow)",
			"    – OcelRevalidationTable (AWS::DynamoDB::Table)",
			"  stack ocel-production-secrets — already current",
			"1 to create, 1 to update, 1 to replace, 1 to delete.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "    stack ocel-production-secrets") {
			t.Errorf("a kept group listed what it keeps; got:\n%s", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the apply the plan described", got)
		}
	})

	t.Run("--dry stops at the plan", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Proposed changes to the production bootstrap") {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to apply.") {
			t.Errorf("stdout = %q, want --dry to say how to apply it", out)
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("--dry reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("a plan that changes nothing applies nothing", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "keep")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing to change — the production bootstrap already matches.") {
			t.Errorf("stdout = %q, want it to say the bootstrap already matches", out)
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("an all-keep plan reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--auto-heal is a change even when the plan holds none", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "keep")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, AutoHealDeclared: true, AutoHeal: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=isr force=false autoHeal=true" {
			t.Errorf("provider saw %v, want the switch recorded even with nothing else to do", got)
		}
	})

	t.Run("a provider that plans nothing still applies", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Nothing to change") {
			t.Errorf("stdout = %q, want a silent plan not to be read as an empty one", stdout.String())
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the apply to go through", got)
		}
	})

	t.Run("a dropped feature the plan deletes carries the force the apply needs", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Features: "isr", FeaturesDeclared: true, Force: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– stack ocel-production-isr") {
			t.Errorf("stdout = %q, want the deletion shown before it is applied", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=isr force=true" {
			t.Errorf("provider saw %v, want the forced drop carried through", got)
		}
	})
}

func TestRunBootstrapPolicy(t *testing.T) {
	t.Run("it writes the document the provider renders for the tier", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := RunPolicy(context.Background(), deps, root, contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, &stdout, &stderr); err != nil {
			t.Fatalf("RunPolicy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "CREDENTIAL_TIER_DEPLOY") {
			t.Errorf("stdout = %q, want the deploy tier's document", stdout.String())
		}
	})
}

func TestBootstrapCarriesAutoHeal(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"an unset switch leaves the account as it is", Options{Yes: true}, "features=isr force=false"},
		{"--auto-heal turns it on", Options{Yes: true, AutoHealDeclared: true, AutoHeal: true}, "features=isr force=false autoHeal=true"},
		{"--auto-heal=false takes it back", Options{Yes: true, AutoHealDeclared: true}, "features=isr force=false autoHeal=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, journal, deps := clitest.SetUpEdgeFixture(t, "")
			t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, tt.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			got := clitest.ReadJournal(t, journal)
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("provider saw %v, want %q", got, tt.want)
			}
		})
	}
}

func TestRunBootstrapStatus(t *testing.T) {
	t.Run("it reports both classes", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), deps, root, StatusOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runBootstrapStatus err = %v; stderr=%s", err, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"production: schema 1", "ocel-bootstrap-isr", "ocel-bootstrap-image-optimization", "preview: not bootstrapped"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("--check fails on stale content", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "stale")
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		err := RunStatus(context.Background(), deps, root, StatusOptions{Check: true}, &stdout, &stderr)
		if err == nil {
			t.Fatalf("--check passed a bootstrap carrying stale content; stdout=%s", stdout.String())
		}
		if !strings.Contains(err.Error(), "ocel-bootstrap-isr") {
			t.Errorf("err = %v, want it to name the stale stack", err)
		}
	})

	t.Run("--check passes a bootstrap this build wrote", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), deps, root, StatusOptions{Check: true}, &stdout, &stderr); err != nil {
			t.Fatalf("--check = %v, want it to pass; stdout=%s", err, stdout.String())
		}
	})
}
