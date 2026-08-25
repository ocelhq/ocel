package bootstrap

import (
	"bytes"
	"context"
	"io"
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

	t.Run("--dry prints the plan, removes nothing, and needs no terminal", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "This will permanently remove the production bootstrap") {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to remove it", out)
		}
		if strings.Contains(out, "TEARDOWN") {
			t.Errorf("stdout = %q, want --dry to remove nothing", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the plan alone", got)
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
			"~ ocel-production-core  [core]",
			"    ~ OcelRouterFunction  AWS::Lambda::Function",
			"    ± OcelOriginSecret    AWS::SecretsManager::Secret   — rotation forces replacement",
			"+ ocel-production-image-optimization  [image-optimization]",
			"– ocel-production-isr  [isr]  — web, api were deployed against it (this one is slow)",
			"    – OcelRevalidationTable  AWS::DynamoDB::Table",
			"  ocel-production-secrets  [secrets]  — already current",
			"1 to create, 1 to update, 1 to replace, 1 to delete.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "    ocel-production-secrets") {
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

	t.Run("a plan that changes nothing still applies, because the apply is the repair path", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "keep")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "No infrastructure changes — applying refreshes bootstrap seals and records.") {
			t.Errorf("stdout = %q, want it to say what applying an all-keep plan is still for", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the apply that refreshes what no plan group covers", got)
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
		if !strings.Contains(stdout.String(), "– ocel-production-isr") {
			t.Errorf("stdout = %q, want the deletion shown before it is applied", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=isr force=true acceptReplacements=true" {
			t.Errorf("provider saw %v, want the forced drop carried through", got)
		}
	})
}

func TestBootstrapYesMeansYes(t *testing.T) {
	t.Run("an interactive yes on a plan holding a replacement carries the consent it needs", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "isr", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "± OcelOriginSecret") {
			t.Fatalf("stdout = %q, want the replacement shown before the confirm that covers it", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || !strings.Contains(got[0], "acceptReplacements=true") {
			t.Errorf("provider saw %v, want a yes on a plan showing ± to reach it as consent to the replacement", got)
		}
	})

	t.Run("a yes on a plan that shows the delete is consent to the delete", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "image-optimization", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– ocel-production-isr") {
			t.Fatalf("stdout = %q, want the delete shown before the confirm that covers it", stdout.String())
		}
		if strings.Contains(stdout.String(), "Remove it anyway?") {
			t.Errorf("stdout = %q, want the plan's own delete group to be the question, asked once", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || !strings.Contains(got[0], "force=true") {
			t.Errorf("provider saw %v, want the drop the plan showed carried through", got)
		}
	})

	t.Run("a silent plan asks about the drop in its own words, and a no stops there", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "none", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Removing isr from the production bootstrap tears down what it stood up.",
			"No project deployed here has recorded needing it",
			"Aborted.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("a refused drop reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})
}

func TestBootstrapDryPreviewsEverything(t *testing.T) {
	t.Run("--dry previews a drop instead of demanding --force", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true, Features: "isr", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– ocel-production-isr") {
			t.Errorf("stdout = %q, want --dry to show what the drop takes", stdout.String())
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("--dry reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--dry previews a drop a silent provider draws no plan for", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true, Features: "none", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Removing isr from the production bootstrap tears down what it stood up.") {
			t.Errorf("stdout = %q, want --dry to say what the drop takes even with no plan to render", stdout.String())
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("--dry reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--dry says the content is going backwards", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapEnvVar, "downgrade")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "keep")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "the same shape, older content") {
			t.Errorf("stdout = %q, want --dry to warn about the downgrade it already knows about", stdout.String())
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
		{"an unset switch leaves the account as it is", Options{Yes: true}, "features=isr force=false acceptReplacements=true"},
		{"--auto-heal turns it on", Options{Yes: true, AutoHealDeclared: true, AutoHeal: true}, "features=isr force=false acceptReplacements=true autoHeal=true"},
		{"--auto-heal=false takes it back", Options{Yes: true, AutoHealDeclared: true}, "features=isr force=false acceptReplacements=true autoHeal=false"},
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
