package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func stubGit(d *deps, branch, pr string) {
	d.currentGitBranch = func(string) (string, error) { return branch, nil }
	d.discoverPRNumber = func() string { return pr }
}

var errNotARepo = errors.New("determine current git branch: not a git repository")

func TestRunPreviewUp(t *testing.T) {
	t.Run("an ephemeral preview sends a preview, ephemeral environment", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("feature/login", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	})

	t.Run("an app's functions reach the preview manifest", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		d := defaultDeps()
		setLoggedIn(&d)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")
		stubAppFunctions(&d, []manifestbuilder.Function{
			{
				Name:         "api",
				Runtime:      "nodejs24.x",
				Handler:      "index.handler",
				ArtifactPath: "output/api",
				Framework:    "express",
				App:          "api",
			},
		})

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "FUNCTION logical_name=api runtime=nodejs24.x handler=index.handler artifact_path=output/api framework=express app=api") {
			t.Errorf("stdout = %q, want the function to have reached the preview manifest", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--ref stands up the explicit ref's ephemeral", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "some-other-branch", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("release/v2", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), d, root, previewUpOptions{ref: "release/v2"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DEPLOY class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key+" source=IDENTITY_SOURCE_GIT") {
			t.Errorf("stdout = %q, want the ephemeral Deploy echo for the explicit ref", out)
		}
	})

	t.Run("--ref needs no git", func(t *testing.T) {
		d := defaultDeps()
		d.currentGitBranch = func(string) (string, error) { return "", errNotARepo }

		env, err := resolveUpEnvironment(d, "", previewUpOptions{ref: "/tmp/some-fixture"})
		if err != nil {
			t.Fatalf("resolveUpEnvironment(--ref) err = %v, want it to resolve without git", err)
		}
		want, err := previewid.Resolve("/tmp/some-fixture", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}
		if env.GetIdentity() != want.Key {
			t.Errorf("identity = %q, want %q", env.GetIdentity(), want.Key)
		}
		if env.GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
			t.Errorf("lifecycle = %v, want ephemeral", env.GetLifecycle())
		}
		if env.GetIdentitySource() != deploymentsv1.Environment_IDENTITY_SOURCE_GIT {
			t.Errorf("identity source = %v, want git", env.GetIdentitySource())
		}
	})

	t.Run("a persistent --name sends a persistent, declared environment", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), d, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DEPLOY class=CLASS_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging source=IDENTITY_SOURCE_DECLARED") {
			t.Errorf("stdout = %q, want the persistent/declared Environment echo", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("it declares the slug and the preview wildcard", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app domains=*.preview.acme.com class=CLASS_PREVIEW") {
			t.Errorf("stdout = %q, want the slug and preview wildcard to have reached Preflight under the preview class", stdout.String())
		}
	})

	t.Run("it refuses without a preview domain, before anything is built", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
};
`)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-preview-domain refusal")
		}
		for _, want := range []string{"declares no preview domain", "domains.preview", "*.preview."} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}

		out := stdout.String()
		if strings.Contains(out, "Building project") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a class mismatch refuses and drives no Deploy", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a class-mismatch error")
		}
		if !strings.Contains(stdout.String(), "ocel preview can only run against preview infrastructure") {
			t.Errorf("stdout = %q, want the concrete class-mismatch message", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})

	t.Run("absent infrastructure refuses and drives no Deploy", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "0")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), d, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(stdout.String(), "ocel bootstrap --preview") {
			t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap --preview`", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runPreviewUp(context.Background(), defaultDeps(), root, previewUpOptions{name: "staging"}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})
}

func TestRunDeployWithoutAPreviewDomain(t *testing.T) {
	t.Run("a production deploy needs no preview domain", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
};
`)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	})
}

func TestRunPreviewRm(t *testing.T) {
	t.Run("an ephemeral preview for the current branch is destroyed without prompting", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("feature/login", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), d, root, previewRmOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DESTROY project=test-app class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
			t.Errorf("stdout = %q, want the ephemeral Destroy echo for the current branch", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want no prompt for ephemeral teardown", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--ref destroys the explicit ref", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "some-other-branch", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("release/v2", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), d, root, previewRmOptions{ref: "release/v2"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "DESTROY project=test-app class=CLASS_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
			t.Errorf("stdout = %q, want the Destroy echo for the explicit ref", stdout.String())
		}
	})

	t.Run("a persistent preview with --yes is destroyed without prompting", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		stubGit(&d, "feature/login", "")
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), d, root, previewRmOptions{name: "staging", yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DESTROY project=test-app class=CLASS_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging source=IDENTITY_SOURCE_DECLARED") {
			t.Errorf("stdout = %q, want the persistent Destroy echo", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to skip the prompt", out)
		}
	})
}

func TestRunPreviewLs(t *testing.T) {
	t.Run("it renders every environment", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewLs(context.Background(), d, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPreviewLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{
			"feature_login_ab12cd34", "ephemeral", "pr-7",
			"staging", "persistent", "—",
			"project:test-app",
		} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		waitForNoStaleSocket(t, sockPath)
	})
}

func TestPreviewEnvironmentFlags(t *testing.T) {
	t.Parallel()

	t.Run("--name and --ref are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name    string
			resolve func() error
		}{
			{"up", func() error {
				_, err := resolveUpEnvironment(defaultDeps(), "", previewUpOptions{name: "staging", ref: "release/v2"})
				return err
			}},
			{"rm", func() error {
				_, err := resolveRmEnvironment(defaultDeps(), "", previewRmOptions{name: "staging", ref: "release/v2"})
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				err := tc.resolve()
				if err == nil {
					t.Fatal("resolve(name+ref) err = nil, want a mutual-exclusion error")
				}
				if !strings.Contains(err.Error(), "mutually exclusive") {
					t.Errorf("err = %v, want it to explain --name and --ref are mutually exclusive", err)
				}
			})
		}
	})

	t.Run("a persistent preview name is capped for the subdomain label", func(t *testing.T) {
		t.Parallel()

		atCap := "a" + strings.Repeat("b", 62)
		overCap := atCap + "c"

		cases := []struct {
			name    string
			resolve func(string) error
		}{
			{"up", func(n string) error {
				_, err := resolveUpEnvironment(defaultDeps(), "", previewUpOptions{name: n})
				return err
			}},
			{"rm", func(n string) error {
				_, err := resolveRmEnvironment(defaultDeps(), "", previewRmOptions{name: n})
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				if err := tc.resolve(atCap); err != nil {
					t.Errorf("--name of %d chars rejected: %v", len(atCap), err)
				}
				err := tc.resolve(overCap)
				if err == nil {
					t.Fatalf("--name of %d chars accepted, want a rejection", len(overCap))
				}
				if !strings.Contains(err.Error(), "too long") {
					t.Errorf("err = %v, want it to say the name is too long", err)
				}
			})
		}
	})
}

func TestConfirmDestroyPreview(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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
