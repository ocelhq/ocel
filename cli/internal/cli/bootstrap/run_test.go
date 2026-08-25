package bootstrap

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/removalplan"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRunBootstrapDestroy(t *testing.T) {
	t.Run("it renders the plan, kept items included, and takes the class name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := Options{}
		if err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PREVIEW, opts, &stdout, &stderr, strings.NewReader("preview\n")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the cloudfront edge",
			"delete edge bootstrap cloudfront",
			"delete bucket ocel-state-preview",
			"(this one is slow)",
			"Left in place:",
			"keep parameter /ocel/pulumi/passphrase — the production bootstrap still stands",
			"Type the environment name (preview) to confirm:",
			"TEARDOWN tier=TIER_PREVIEW",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Index(out, "keep parameter") < strings.Index(out, "This cannot be undone.") {
			t.Errorf("kept items must follow the doomed ones; got:\n%s", out)
		}
	})

	t.Run("a phrase that is not the class name aborts", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := Options{}
		if err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("preview\n")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("stdout = %q, want the teardown aborted", out)
		}
		if strings.Contains(out, "TEARDOWN") {
			t.Errorf("stdout = %q, want no teardown after an unconfirmed phrase", out)
		}
	})

	t.Run("--yes skips the phrase and the terminal requirement", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		opts := Options{Yes: true}
		if err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
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
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		t.Setenv(removalplan.BypassEnv, "production")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		if err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Type the environment name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed phrase", stdout.String())
		}
		if !strings.Contains(stderr.String(), removalplan.BypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed teardown is never silent", stderr.String(), removalplan.BypassEnv)
		}
	})

	t.Run("a bypass naming the other bootstrap is refused, not ignored", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		t.Setenv(removalplan.BypassEnv, "preview")

		var stdout, stderr bytes.Buffer
		opts := Options{}
		err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the mismatched-bypass refusal")
		}
		for _, want := range []string{removalplan.BypassEnv, "preview", "production"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("without a terminal, a phrase it cannot ask for is a refusal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		opts := Options{}
		err := RunDestroy(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the no-terminal refusal")
		}
		for _, want := range []string{"interactive terminal", "--yes", removalplan.BypassEnv} {
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

		err := Run(context.Background(), clitest.NewSession(), t.TempDir(), environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
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

		err := Run(context.Background(), clitest.NewSession(), root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
	})
}

func TestRunBootstrapPolicy(t *testing.T) {
	t.Run("it writes the document the provider renders for the tier", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		if err := RunPolicy(context.Background(), sess, root, contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, &stdout, &stderr); err != nil {
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
			root, journal, sess := clitest.SetUpEdgeFixture(t, "")
			t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, tt.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
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
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), sess, root, StatusOptions{}, &stdout, &stderr); err != nil {
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
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		err := RunStatus(context.Background(), sess, root, StatusOptions{Check: true}, &stdout, &stderr)
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
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), sess, root, StatusOptions{Check: true}, &stdout, &stderr); err != nil {
			t.Fatalf("--check = %v, want it to pass; stdout=%s", err, stdout.String())
		}
	})
}

func TestRunBootstrapDowngrade(t *testing.T) {
	t.Run("it warns and takes a confirmation before writing older content", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "downgrade")
		sess := clitest.NewSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "none", FeaturesDeclared: true}
		if err := Run(context.Background(), sess, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "last written by 1.9.0") {
			t.Errorf("stdout never named the newer build that wrote the bootstrap; got:\n%s", out)
		}
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("declining the downgrade still wrote; got:\n%s", out)
		}
	})
}
