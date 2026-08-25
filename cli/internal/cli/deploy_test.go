package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestConfirmDeploy(t *testing.T) {
	t.Parallel()

	answers := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"word yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"explicit no", "n\n", false},
		{"empty answer defaults to no", "\n", false},
		{"unrecognized answer defaults to no", "sure\n", false},
		{"no input at all defaults to no", "", false},
	}
	for _, tc := range answers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			got, err := confirmDeploy(context.Background(), "my-app", "@ocel/provider-aws", nil, &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDeploy(context.Background(), ) error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDeploy(context.Background(), %q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), "Deploy my-app with @ocel/provider-aws? [y/N]") {
				t.Errorf("stdout = %q, want it to contain the confirm prompt", stdout.String())
			}
		})
	}

	t.Run("an unrecognized slug warns instead of refusing", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		got, err := confirmDeploy(context.Background(), "my-app", "@ocel/provider-aws", []string{"my-application", "billing"}, &stdout, strings.NewReader("y\n"))
		if err != nil {
			t.Fatalf("confirmDeploy(context.Background(), ) error = %v", err)
		}
		if !got {
			t.Error("confirmDeploy(context.Background(), ) = false, want the y answer to still proceed — the guard is a warning, not a refusal")
		}

		out := stdout.String()
		for _, want := range []string{
			`No existing deployment for slug "my-app".`,
			"This will create a NEW project.",
			"This backend already has: my-application, billing",
			"Continue? [y/N]",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Deploy my-app with") {
			t.Errorf("stdout = %q, want the routine update prompt replaced, not appended to", out)
		}
	})

	t.Run("the unrecognized-slug prompt defaults to no", func(t *testing.T) {
		t.Parallel()

		for _, input := range []string{"\n", "", "sure\n"} {
			var stdout bytes.Buffer
			got, err := confirmDeploy(context.Background(), "my-app", "@ocel/provider-aws", []string{"my-application"}, &stdout, strings.NewReader(input))
			if err != nil {
				t.Fatalf("confirmDeploy(context.Background(), %q) error = %v", input, err)
			}
			if got {
				t.Errorf("confirmDeploy(context.Background(), %q) = true, want the drift prompt to default to No", input)
			}
		}
	})

	t.Run("an empty backend is not nagged", func(t *testing.T) {
		t.Parallel()

		for _, known := range [][]string{nil, {}} {
			var stdout bytes.Buffer
			if _, err := confirmDeploy(context.Background(), "my-app", "@ocel/provider-aws", known, &stdout, strings.NewReader("y\n")); err != nil {
				t.Fatalf("confirmDeploy(context.Background(), ) error = %v", err)
			}
			out := stdout.String()
			if !strings.Contains(out, "Deploy my-app with @ocel/provider-aws? [y/N]") {
				t.Errorf("stdout = %q, want the routine prompt", out)
			}
			if strings.Contains(out, "NEW project") {
				t.Errorf("stdout = %q, want no drift warning when the backend reports nothing", out)
			}
		}
	})
}

func TestToDeclarations(t *testing.T) {
	t.Parallel()

	t.Run("maps resource fields", func(t *testing.T) {
		t.Parallel()

		resources := []declare.Resource{
			{
				Name:     "main",
				Type:     linksv1.LinkType_LINK_TYPE_POSTGRES,
				Postgres: &resourcesv1.PostgresConfig{Version: "17"},
			},
		}

		decls := toDeclarations(t.TempDir(), resources)

		if len(decls) != 1 {
			t.Fatalf("len(decls) = %d, want 1", len(decls))
		}
		d := decls[0]
		if d.ID != "main" {
			t.Errorf("ID = %q, want %q", d.ID, "main")
		}
		if d.Type != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Errorf("Type = %v, want %v", d.Type, linksv1.LinkType_LINK_TYPE_POSTGRES)
		}
		if d.Postgres.GetVersion() != "17" {
			t.Errorf("Postgres.Version = %q, want %q", d.Postgres.GetVersion(), "17")
		}
	})

	t.Run("reads the declaring file out of the reported stack", func(t *testing.T) {
		t.Parallel()

		configDir := t.TempDir()
		resources := []declare.Resource{{
			Name:  "main",
			Type:  linksv1.LinkType_LINK_TYPE_POSTGRES,
			Stack: "Error\n    at Postgres (" + filepath.Join(configDir, "shared", "db.ts") + ":3:15)",
		}}

		decls := toDeclarations(configDir, resources)

		if len(decls) != 1 {
			t.Fatalf("len(decls) = %d, want 1", len(decls))
		}
		if decls[0].Source != "shared/db.ts:3" {
			t.Errorf("Source = %q, want %q", decls[0].Source, "shared/db.ts:3")
		}
	})
}

func TestRunDeploy(t *testing.T) {
	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		err := runDeploy(context.Background(), newSession(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("a malformed config errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

		err := runDeploy(context.Background(), newSession(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.config.ts") {
			t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runDeploy(context.Background(), newSession(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})

	t.Run("the happy path discovers, builds, spawns and deploys to success", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()

		t.Run("streams the provider's progress", func(t *testing.T) {
			if !strings.Contains(out, "provisioning...") {
				t.Errorf("stdout = %q, want it to contain the streamed progress event", out)
			}
		})
		t.Run("ends on a terminal success message", func(t *testing.T) {
			if !strings.Contains(out, "Deployed") {
				t.Errorf("stdout = %q, want a terminal success message", out)
			}
		})
		t.Run("sends a production environment", func(t *testing.T) {
			if !strings.Contains(out, "DEPLOY tier=TIER_PRODUCTION lifecycle=LIFECYCLE_UNSPECIFIED") {
				t.Errorf("stdout = %q, want deploy to send a production Environment", out)
			}
		})
		t.Run("skips the confirm prompt under --yes", func(t *testing.T) {
			if strings.Contains(out, "[y/N]") {
				t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", out)
			}
		})

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("an app builds its functions into the manifest", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{
				Name:         "api",
				Runtime:      "nodejs24.x",
				Handler:      "src/server.js",
				ArtifactPath: "output/api",
				Framework:    "express",
				App:          "api",
			},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "FUNCTION logical_name=fn--api--api runtime=nodejs24.x handler=src/server.js artifact_path=output/api framework=express app=api") {
			t.Errorf("stdout = %q, want the function to have reached the manifest", out)
		}
		if strings.Contains(stderr.String(), "deploying infrastructure only") {
			t.Errorf("stderr = %q, want no infra-only warning when a function is built", stderr.String())
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want a terminal success message", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("no apps warns and deploys resources only", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "no functions to deploy; deploying infrastructure only") {
			t.Errorf("stdout = %q, want the infra-only warning", out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want resources to still deploy to success", out)
		}
		if strings.Contains(out, "FUNCTION ") {
			t.Errorf("stdout = %q, want no function echoed when no apps are configured", out)
		}
		if strings.Contains(out, "APP ") {
			t.Errorf("stdout = %q, want no app echoed when nothing was built", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("an app build failure aborts before spawn", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.BuildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
			return errors.New("boom: app build failed")
		}
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the app-build failure")
		}
		if !strings.Contains(stdout.String(), "boom: app build failed") {
			t.Errorf("stdout = %q, want the app-build failure surfaced", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})

	refusals := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "a class mismatch refuses without deploying",
			env:  map[string]string{clitest.FakeInfraTierEnvVar: "preview", clitest.FakeInfraPresentEnvVar: "1"},
			want: "ocel deploy can only run against production infrastructure",
		},
		{
			name: "absent infrastructure refuses without deploying",
			env:  map[string]string{clitest.FakeInfraPresentEnvVar: "0"},
			want: "ocel bootstrap",
		},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession()
			clitest.SetLoggedIn(&sess)
			clitest.StubBuild(&sess, nil)
			root, _ := clitest.SetUpDeployFixture(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			var stdout, stderr bytes.Buffer
			err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("runDeploy err = nil, want a refusal")
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.want)
			}
			if strings.Contains(stdout.String(), "DEPLOY ") {
				t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
			}
		})
	}

	t.Run("stdin that is not a terminal proceeds without prompting", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if strings.Contains(stdout.String(), "[y/N]") {
			t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("declared domains carry the slug", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "app.acme.com" },
};
`)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app") {
			t.Errorf("stdout = %q, want the preflight to have carried the project's slug", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--yes leaves the slug out", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.StdinIsTerminal = func(io.Reader) bool { return true }
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug= ") {
			t.Errorf("stdout = %q, want --yes to ask for no slug-scoped answers", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a non-TTY stdin leaves the slug out", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug= ") {
			t.Errorf("stdout = %q, want a non-TTY deploy to ask for no slug-scoped answers", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("an interactive deploy warns about other projects", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		sess.StdinIsTerminal = func(io.Reader) bool { return true }
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "PREFLIGHT slug=test-app") {
			t.Errorf("stdout = %q, want the prompting deploy to have asked with the slug", out)
		}
		for _, want := range []string{
			"No existing deployment for slug \"test-app\".",
			"This will create a NEW project.",
			"This backend already has: my-application, billing",
			"Continue? [y/N]",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--yes bypasses the slug drift guard", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "NEW project") || strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to bypass the drift prompt", out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want the deploy to proceed", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("the identity banner prints before the build and the deploy", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		pretendStdoutIsTerminal(&sess)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDProviderEnvVar, "Origin")
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDProfileEnvVar, "default")
		t.Setenv(clitest.FakeIDRegionEnvVar, "us-east-1")
		t.Setenv(clitest.FakeIDEdgeScopeEnvVar, "abcd1234")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"Running with:", "Origin      account=123456789012", "region=us-east-1", "profile=default", "Edge        account=abcd1234"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
		banner := strings.Index(out, "Running with:")
		build := strings.Index(out, "Building project")
		deploy := strings.Index(out, "DEPLOY ")
		if banner < 0 || build < 0 || deploy < 0 {
			t.Fatalf("expected banner, build, and deploy all present; banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
		}
		if banner >= build || build >= deploy {
			t.Errorf("expected order banner < build < deploy; got banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("without a terminal the identity banner is omitted", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDProfileEnvVar, "default")
		t.Setenv(clitest.FakeIDRegionEnvVar, "us-east-1")
		t.Setenv(clitest.FakeIDEdgeScopeEnvVar, "abcd1234")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Deployed") {
			t.Fatalf("stdout = %q, want the deploy to have proceeded", out)
		}
		for _, leaked := range []string{"Running with:", "123456789012", "abcd1234", "profile=default"} {
			if strings.Contains(out+stderr.String(), leaked) {
				t.Errorf("output leaked %q with no terminal to print it to:\n%s", leaked, out)
			}
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a credential problem aborts before the build and the deploy", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		pretendStdoutIsTerminal(&sess)
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDProfileEnvVar, "default")
		t.Setenv(clitest.FakeCredProblemEnvVar, "Cloudflare")

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a credential-check error")
		}

		out := stdout.String()
		if !strings.Contains(out, "account=123456789012") {
			t.Errorf("stdout = %q, want the resolved identity still shown", out)
		}
		if !strings.Contains(out, "Cloudflare") {
			t.Errorf("stdout = %q, want the Cloudflare credential problem surfaced", out)
		}
		if strings.Contains(out, "Building project") {
			t.Errorf("stdout = %q, want the build to be skipped on a credential failure", out)
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a single app produces exactly one attributed app", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [{ name: "api", path: "apps/api", framework: "express", domains: { production: "Api.Acme.com" } }],
};
`)
		writeAppSource(t, root, "api")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "APP "); got != 1 {
			t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name=api framework=express production_domain=api.acme.com") {
			t.Errorf("stdout = %q, want the app with its per-app production domain", out)
		}
		if !strings.Contains(out, "deployment="+clitest.FixtureDeploymentID("api")) {
			t.Errorf("stdout = %q, want the app deployed under the id its build recorded", out)
		}
		if !strings.Contains(out, "framework=express app=api") {
			t.Errorf("stdout = %q, want the function attributed to the api app", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("two apps attribute their functions to their own app", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{Name: "web", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/web", Framework: "express", App: "web"},
			{Name: "admin", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/admin", Framework: "express", App: "admin"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "acme.com" } },
    { name: "admin", path: "apps/admin", framework: "express" },
  ],
};
`)
		writeAppSource(t, root, "web", "admin")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "APP "); got != 2 {
			t.Fatalf("stdout echoed %d apps, want exactly 2:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name=admin framework=express production_domain=") {
			t.Errorf("stdout = %q, want the admin app with no domain of its own", out)
		}
		if !strings.Contains(out, "APP name=web framework=express production_domain=acme.com") {
			t.Errorf("stdout = %q, want the web app with its own production domain", out)
		}
		if !strings.Contains(out, "logical_name=fn--web--web") || !strings.Contains(out, "artifact_path=output/web framework=express app=web") {
			t.Errorf("stdout = %q, want the web function attributed to the web app", out)
		}
		if !strings.Contains(out, "logical_name=fn--admin--admin") || !strings.Contains(out, "artifact_path=output/admin framework=express app=admin") {
			t.Errorf("stdout = %q, want the admin function attributed to the admin app", out)
		}
		for _, app := range []string{"web", "admin"} {
			if !strings.Contains(out, "name="+app+" framework=express production_domain=") {
				t.Errorf("stdout = %q, want %s echoed", out, app)
			}
			if !strings.Contains(out, "deployment="+clitest.FixtureDeploymentID(app)) {
				t.Errorf("stdout = %q, want %s deployed under the id its own build recorded", out, app)
			}
		}
		if clitest.FixtureDeploymentID("web") == clitest.FixtureDeploymentID("admin") {
			t.Fatal("the fixture gives both apps one id, so this proves nothing")
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a detected app appears in the manifest", func(t *testing.T) {
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "output/index", Framework: "next", App: "express-app"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "APP "); got != 1 {
			t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name=express-app framework=next production_domain=") {
			t.Errorf("stdout = %q, want the detected app named in the manifest", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})
}

func pretendStdoutIsTerminal(sess *session.Session) {
	sess.StdoutIsTerminal = func(io.Writer) bool { return true }
}

func addAppToFixtureConfig(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)
	writeAppSource(t, root, "api")
}

func writeAppSource(t *testing.T, root string, apps ...string) {
	t.Helper()
	for _, app := range apps {
		clitest.WriteFile(t, filepath.Join(root, "apps", app, "src", "server.ts"), `
export function handler() {
  return "`+app+`";
}
`)
	}
}

func waitForNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); errors.Is(err, fs.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s", sockPath)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
