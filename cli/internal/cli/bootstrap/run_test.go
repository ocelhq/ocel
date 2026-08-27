package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/runui"
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
		t.Setenv(runui.BypassEnv, "production")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Type the environment name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed phrase", stdout.String())
		}
		if !strings.Contains(stderr.String(), runui.BypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed teardown is never silent", stderr.String(), runui.BypassEnv)
		}
	})

	t.Run("a bypass naming the other bootstrap is refused, not ignored", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(runui.BypassEnv, "preview")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the mismatched-bypass refusal")
		}
		for _, want := range []string{runui.BypassEnv, "preview", "production"} {
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
		for _, want := range []string{
			"This will permanently remove the production bootstrap",
			"– aws/ocel-production-isr  [isr]",
			"    – RevalidationTable  AWS::DynamoDB::Table",
			"– aws/ocel-production  [core]",
			"    – StateBucket  AWS::S3::Bucket   — the Pulumi state of every stack this bootstrap deployed (this one is slow)",
			"– aws/parameters",
			"    – /ocel/origin/secret  AWS::SSM::Parameter",
			"– cloudflare/edge  [cloudflare-edge]",
			"    – ocel-deployments-store  Cloudflare::Worker",
			"5 to delete, 1 unchanged.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want --dry to print %q", out, want)
			}
		}
		if strings.Contains(out, "/ocel/pulumi/passphrase") {
			t.Errorf("stdout = %q, want the kept parameter counted, not listed", out)
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

	t.Run("nothing standing is a clean no-op, not a teardown", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeEmptyRemovalPlanEnvVar, "1")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing to destroy: the production environment is not bootstrapped") {
			t.Errorf("stdout = %q, want it to declare the no-op", out)
		}
		for _, unwanted := range []string{"This will permanently remove", "This cannot be undone", "TEARDOWN"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("stdout = %q, want no %q: an empty plan must stop before the warning and the teardown", out, unwanted)
			}
		}
	})

	t.Run("--dry with nothing standing declares the no-op and offers nothing", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeEmptyRemovalPlanEnvVar, "1")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true}
		if err := RunDestroy(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing to destroy: the production environment is not bootstrapped") {
			t.Errorf("stdout = %q, want it to declare the no-op", out)
		}
		if strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want no offer to destroy what is not there", out)
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
		for _, want := range []string{"needs a terminal", "--yes", runui.BypassEnv} {
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
			"~ aws/ocel-production-core  [core]",
			"    ~ OcelRouterFunction  AWS::Lambda::Function",
			"    ± OcelOriginSecret    AWS::SecretsManager::Secret   — rotation forces replacement",
			"+ aws/ocel-production-image-optimization  [image-optimization]",
			"– aws/ocel-production-isr  [isr]  — web, api were deployed against it (this one is slow)",
			"    – OcelRevalidationTable  AWS::DynamoDB::Table",
			"1 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "aws/ocel-production-secrets") {
			t.Errorf("a group that keeps everything took a row; got:\n%s", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the apply the plan described", got)
		}
	})

	t.Run("the selected edge stands beside the stacks, in its own vocabulary", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\" },\n")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Proposed changes to the production bootstrap, fronted by the cloudflare edge:",
			"+ cloudflare/edge  [cloudflare-edge]",
			"    + ocel-edge-cache         Cloudflare::R2Bucket",
			"    + ocel-deployments-store  Cloudflare::Worker",
			"3 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "edge cloudflare/edge") {
			t.Errorf("the group named its kind on top of its vendor prefix; got:\n%s", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) == 0 {
			t.Error("provider saw nothing, want the apply the plan described")
		}
	})

	t.Run("credentials the plan cannot reach stop the run before it prints half a plan", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\" },\n")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "edge-credentials")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true}
		err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runBootstrap err = nil, want the missing credential to stop the plan; stdout=%s", stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "CLOUDFLARE_ACCOUNT_ID is not set") {
			t.Errorf("stdout = %q, want the failure to name the variable that is missing", out)
		}
		if strings.Contains(out, "Proposed changes") {
			t.Errorf("a plan missing its edge was printed anyway; got:\n%s", out)
		}
		if strings.Contains(out, "Run without --dry to apply.") {
			t.Errorf("a failed plan offered the apply; got:\n%s", out)
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("a failed plan reached the provider: %v", clitest.ReadJournal(t, journal))
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
		if !strings.Contains(stdout.String(), "No infrastructure changes — applying refreshes bootstrap seals and records.") {
			t.Errorf("stdout = %q, want the confirm never to float over a void", stdout.String())
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the apply to go through", got)
		}
	})

	t.Run("--remove carries the force the apply needs, and leaves the rest standing", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Remove: "isr", Force: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– aws/ocel-production-isr") {
			t.Errorf("stdout = %q, want the deletion shown before it is applied", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=image-optimization remove=isr force=true acceptReplacements=true" {
			t.Errorf("provider saw %v, want the named removal carried through and the unnamed feature ensured", got)
		}
	})

	t.Run("a standing feature no flag names is not the subject of the plan", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Features: "isr", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=isr force=false acceptReplacements=true" {
			t.Errorf("provider saw %v, want image-optimization neither ensured nor removed by a run that never named it", got)
		}
	})

	t.Run("--features and --remove naming the same feature is refused before any plan", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Features: "isr", FeaturesDeclared: true, Remove: "isr"}
		err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want a feature named both ways refused")
		}
		for _, want := range []string{"--features", "--remove", "isr"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want the refusal to name %q", stdout.String(), want)
			}
		}
		if _, statErr := os.Stat(journal); statErr == nil {
			t.Errorf("the provider was reached despite the contradiction: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--remove of a feature that is not standing says so and stops", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Remove: "image-optimization"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "image-optimization is not in the production bootstrap, so there is nothing to remove.") {
			t.Errorf("stdout = %q, want it to say the named feature was never there", stdout.String())
		}
		if _, statErr := os.Stat(journal); statErr == nil {
			t.Errorf("provider saw %v, want a run that was only a removal of nothing to apply nothing", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--remove of an absent feature beside --features still ensures what --features named", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Remove: "image-optimization", Features: "isr", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=isr force=false acceptReplacements=true" {
			t.Errorf("provider saw %v, want the declared features ensured despite the empty removal", got)
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

	t.Run("one plan and one yes cover an add and a removal together", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "image-optimization", FeaturesDeclared: true, Remove: "isr"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– aws/ocel-production-isr") {
			t.Fatalf("stdout = %q, want the delete shown before the confirm that covers it", stdout.String())
		}
		if strings.Contains(stdout.String(), "Remove it anyway?") {
			t.Errorf("stdout = %q, want the plan's own delete group to be the question, asked once", stdout.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || got[0] != "features=image-optimization remove=isr force=true acceptReplacements=true" {
			t.Errorf("provider saw %v, want the add and the removal in the one apply the plan covered", got)
		}
	})

	t.Run("a silent plan still says what the removal takes, and a no stops there", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "none", FeaturesDeclared: true, Remove: "isr"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Removing isr from the production bootstrap tears down what it stood up.",
			"Aborted.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("a refused removal reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("what the removal takes rides the stream, so a json run consents to something it was shown", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")
		deps.Presentation = func(io.Writer) runui.Presentation {
			return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
		}
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Features: "none", FeaturesDeclared: true, Remove: "isr"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		want := "Removing isr from the production bootstrap tears down what it stood up."
		said := streamDiagnostics(t, stdout.String())
		if !slices.Contains(said, want) {
			t.Errorf("the stream said %v, want it to carry %q — the consent that follows covers it", said, want)
		}
		if strings.Contains(withoutEnvelopes(stdout.String()), want) {
			t.Errorf("the disclosure also went out beside the stream; got:\n%s", stdout.String())
		}
	})
}

func streamDiagnostics(t *testing.T, out string) []string {
	t.Helper()
	var said []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Diagnostic struct {
				Message string `json:"message"`
			} `json:"diagnostic"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Diagnostic.Message != "" {
			said = append(said, ev.Diagnostic.Message)
		}
	}
	return said
}

func withoutEnvelopes(out string) string {
	var loose []string
	for _, line := range strings.Split(out, "\n") {
		if !json.Valid([]byte(line)) {
			loose = append(loose, line)
		}
	}
	return strings.Join(loose, "\n")
}

func TestBootstrapDryPreviewsEverything(t *testing.T) {
	t.Run("--dry previews a removal instead of demanding --force", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "mixed")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true, Remove: "isr"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– aws/ocel-production-isr") {
			t.Errorf("stdout = %q, want --dry to show what the removal takes", stdout.String())
		}
		if _, err := os.Stat(journal); err == nil {
			t.Errorf("--dry reached the provider: %v", clitest.ReadJournal(t, journal))
		}
	})

	t.Run("--dry previews a removal a silent provider draws no plan for", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")
		t.Setenv(clitest.FakeBootstrapPlanEnvVar, "silent")

		var stdout, stderr bytes.Buffer
		opts := Options{Dry: true, Remove: "isr"}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Removing isr from the production bootstrap tears down what it stood up.") {
			t.Errorf("stdout = %q, want --dry to say what the removal takes even with no plan to render", stdout.String())
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

func TestBootstrapSaysWhatItAppliedBeyondWhatWasAsked(t *testing.T) {
	t.Run("a cloudflare project told to apply nothing is told what its edge pulls in", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\" },\n")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true, Features: noFeatures, FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		want := "Also adding: cloudflare-edge — this project's edge needs it\nAlso adding: isr — cloudflare-edge needs it\n"
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to carry %q", stdout.String(), want)
		}
	})

	t.Run("a bare run on the default edge is told its edge feature too", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		want := "Also adding: cloudfront-edge — this project's edge needs it\n"
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to carry %q", stdout.String(), want)
		}
	})

	t.Run("a set that names everything applied says nothing", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\" },\n")

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true, Dry: true, Features: "isr,cloudflare-edge", FeaturesDeclared: true}
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Also adding:") {
			t.Errorf("stdout = %q, want nothing said where the set named everything applied", stdout.String())
		}
	})
}
