package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func stubGit(sess *session.Session, branch, pr string) {
	sess.CurrentGitBranch = func(string) (string, error) { return branch, nil }
	sess.DiscoverPRNumber = func() string { return pr }
}

var errNotARepo = errors.New("determine current git branch: not a git repository")

func TestRunPreviewUp(t *testing.T) {
	t.Run("an ephemeral preview sends a preview, ephemeral environment", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("feature/login", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{
			"DEPLOY tier=TIER_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL",
			"identity=" + want.Key,
			"Preview " + want.Key + " is up",
		} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("an app's functions reach the preview manifest", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{
				Route:        "api",
				Runtime:      "nodejs24.x",
				Handler:      "index.handler",
				ArtifactPath: "output/api",
				Framework:    "express",
				App:          "api",
			},
		})

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "FUNCTION logical_name=fn--api--api runtime=nodejs24.x handler=index.handler artifact_path=output/api framework=express app=api") {
			t.Errorf("stdout = %q, want the function to have reached the preview manifest", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a preview publishes a map whose edges are the manifest's usages", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		clitest.StubBuild(&sess, []manifestbuilder.Function{
			{Route: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readServiceMap(t, root)
		want := []servicemap.Usage{{App: "api", Resource: "db--main", Files: []string{"apps/api/src/server.ts"}}}
		if !reflect.DeepEqual(got.Usages, want) {
			t.Errorf("usages = %+v, want %+v", got.Usages, want)
		}
		if got.Environment.Class != "preview" {
			t.Errorf("environment = %+v, want the preview's own context", got.Environment)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--ref stands up the explicit ref's ephemeral", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "some-other-branch", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("release/v2", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{ref: "release/v2"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DEPLOY tier=TIER_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
			t.Errorf("stdout = %q, want the ephemeral Deploy echo for the explicit ref", out)
		}
	})

	t.Run("--ref needs no git", func(t *testing.T) {
		sess := newSession()
		sess.CurrentGitBranch = func(string) (string, error) { return "", errNotARepo }

		env, err := resolveUpEnvironment(sess, "", previewUpOptions{ref: "/tmp/some-fixture"})
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
		if env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
			t.Errorf("lifecycle = %v, want ephemeral", env.GetLifecycle())
		}
	})

	t.Run("a persistent --name sends a persistent, declared environment", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DEPLOY tier=TIER_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging") {
			t.Errorf("stdout = %q, want the persistent/declared Environment echo", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("it declares the slug and the preview wildcard", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app domains=*.preview.acme.com tier=TIER_PREVIEW") {
			t.Errorf("stdout = %q, want the slug and preview wildcard to have reached Preflight under the preview class", stdout.String())
		}
	})

	t.Run("it refuses without a preview domain, before anything is built", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
};
`)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-preview-domain refusal")
		}
		out := stdout.String()
		for _, want := range []string{"declares no preview domain", "domains.preview", "*.preview.", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}

		if strings.Contains(out, "Building project") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a project with no preview domain serves on the bootstrap's global one", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
};
`)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "previews.ocel.dev")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"global preview domain *.previews.ocel.dev", "DEPLOY tier=TIER_PREVIEW"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a class mismatch refuses and drives no Deploy", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
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
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(stdout.String(), "ocel bootstrap preview") {
			t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap preview`", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runPreviewUp(context.Background(), newSession(), root, previewUpOptions{name: "staging"}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
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
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
};
`)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	})
}

func TestRunPreviewRm(t *testing.T) {
	t.Run("an ephemeral preview for the current branch is destroyed without prompting", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("feature/login", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), sess, root, previewRmOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DESTROY project=test-app tier=TIER_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
			t.Errorf("stdout = %q, want the ephemeral Destroy echo for the current branch", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want no prompt for ephemeral teardown", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--ref destroys the explicit ref", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "some-other-branch", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		want, err := previewid.Resolve("release/v2", "")
		if err != nil {
			t.Fatalf("previewid.Resolve: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), sess, root, previewRmOptions{ref: "release/v2"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "DESTROY project=test-app tier=TIER_PREVIEW lifecycle=LIFECYCLE_EPHEMERAL identity="+want.Key) {
			t.Errorf("stdout = %q, want the Destroy echo for the explicit ref", stdout.String())
		}
	})

	t.Run("a persistent preview with --yes is destroyed without prompting", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewRm(context.Background(), sess, root, previewRmOptions{name: "staging", yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "DESTROY project=test-app tier=TIER_PREVIEW lifecycle=LIFECYCLE_PERSISTENT identity=staging") {
			t.Errorf("stdout = %q, want the persistent Destroy echo", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to skip the prompt", out)
		}
	})
}

func TestRunPreviewLs(t *testing.T) {
	t.Run("it renders every environment", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPreviewLs(context.Background(), sess, root, &stdout, &stderr); err != nil {
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
				_, err := resolveUpEnvironment(newSession(), "", previewUpOptions{name: "staging", ref: "release/v2"})
				return err
			}},
			{"rm", func() error {
				_, err := resolveRmEnvironment(newSession(), "", previewRmOptions{name: "staging", ref: "release/v2"})
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
				_, err := resolveUpEnvironment(newSession(), "", previewUpOptions{name: n})
				return err
			}},
			{"rm", func(n string) error {
				_, err := resolveRmEnvironment(newSession(), "", previewRmOptions{name: n})
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

func TestPreviewPreflightShapeKeepsTeardownOffTheSharedWildcardRefusal(t *testing.T) {
	const why = "the provider refuses a global-preview account mismatch only for a preflight that carries a slug and no domains, " +
		"because that is exactly a preview deploy landing on the shared wildcard; a teardown that starts sending a slug would be refused and strand its resources"

	setUpPreview := func(t *testing.T) (root, journal string, sess session.Session) {
		t.Helper()
		root, _ = clitest.SetUpDeployFixture(t)
		journal = filepath.Join(t.TempDir(), "preflight.journal")
		t.Setenv(clitest.FakePreflightJournalEnvVar, journal)
		sess = newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubBuild(&sess, nil)
		stubGit(&sess, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		return root, journal, sess
	}

	t.Run("up sends the slug and the project's declared preview hostnames, so the shared-wildcard refusal can reach it", func(t *testing.T) {
		root, journal, sess := setUpPreview(t)

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), sess, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readPreflightJournal(t, journal)
		if len(got) != 1 {
			t.Fatalf("`ocel preview up` issued %d preflights, want exactly 1: %+v", len(got), got)
		}
		if got[0].slug == "" {
			t.Errorf("`ocel preview up` sent an empty slug, want the project slug: %s", why)
		}
		if want := "*.preview.acme.com"; strings.Join(got[0].domains, ",") != want {
			t.Errorf("`ocel preview up` sent domains %v, want the project's declared preview hostnames [%s]: %s", got[0].domains, want, why)
		}
	})

	teardowns := []struct {
		name string
		run  func(t *testing.T, sess session.Session, root string, stdout, stderr *bytes.Buffer) error
	}{
		{"rm", func(t *testing.T, sess session.Session, root string, stdout, stderr *bytes.Buffer) error {
			return runPreviewRm(context.Background(), sess, root, previewRmOptions{}, stdout, stderr, strings.NewReader(""))
		}},
		{"prune", func(t *testing.T, sess session.Session, root string, stdout, stderr *bytes.Buffer) error {
			return runPreviewPrune(context.Background(), sess, root, previewPruneOptions{name: "staging", keep: defaultPreviewPruneKeepN}, stdout, stderr)
		}},
	}
	for _, tc := range teardowns {
		t.Run(tc.name+" sends neither a slug nor domains, so the shared-wildcard refusal never reaches a teardown", func(t *testing.T) {
			root, journal, sess := setUpPreview(t)

			var stdout, stderr bytes.Buffer
			if err := tc.run(t, sess, root, &stdout, &stderr); err != nil {
				t.Fatalf("`ocel preview %s` err = %v; stdout=%s stderr=%s", tc.name, err, stdout.String(), stderr.String())
			}

			got := readPreflightJournal(t, journal)
			if len(got) != 1 {
				t.Fatalf("`ocel preview %s` issued %d preflights, want exactly 1: %+v", tc.name, len(got), got)
			}
			if got[0].slug != "" {
				t.Errorf("`ocel preview %s` sent slug %q, want none: %s", tc.name, got[0].slug, why)
			}
			if len(got[0].domains) != 0 {
				t.Errorf("`ocel preview %s` sent domains %v, want none: %s", tc.name, got[0].domains, why)
			}
		})
	}

	t.Run("ls preflights not at all, so nothing about it can be refused", func(t *testing.T) {
		root, journal, sess := setUpPreview(t)

		var stdout, stderr bytes.Buffer
		if err := runPreviewLs(context.Background(), sess, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPreviewLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if got := readPreflightJournal(t, journal); len(got) != 0 {
			t.Errorf("`ocel preview ls` issued %d preflights, want none: %+v; %s", len(got), got, why)
		}
	})
}

type preflightRecord struct {
	slug    string
	domains []string
	class   string
}

func readPreflightJournal(t *testing.T, path string) []preflightRecord {
	t.Helper()

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read preflight journal %s: %v", path, err)
	}

	var records []preflightRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec preflightRecord
		for _, field := range strings.Fields(line) {
			key, value, _ := strings.Cut(field, "=")
			switch key {
			case "slug":
				rec.slug = value
			case "domains":
				if value != "" {
					rec.domains = strings.Split(value, ",")
				}
			case "class":
				rec.class = value
			}
		}
		records = append(records, rec)
	}
	return records
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
			got, err := confirmDestroyPreview(context.Background(), "staging", &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDestroyPreview(context.Background(), ) error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDestroyPreview(context.Background(), %q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), `Destroy persistent preview "staging"? [y/N]`) {
				t.Errorf("stdout = %q, want the persistent destroy prompt", stdout.String())
			}
		})
	}
}

func TestRequirePreviewDomain(t *testing.T) {
	t.Parallel()

	declared := &projectconfig.Config{Domains: map[string][]string{"preview": {"*.preview.acme.com"}}}
	bare := &projectconfig.Config{}
	global := &contractv1.PreviewWildcard{
		BaseDomain:     "previews.ocel.dev",
		GrammarMin:     1,
		GrammarMax:     1,
		RouteInstalled: true,
	}

	t.Run("neither a global domain nor a declared one refuses", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		err := requirePreviewDomain(bare, nil, nil, "pr-1", &out)
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a refusal")
		}
		for _, want := range []string{"declares no preview domain", "no global one", "domains.preview", "ocel domain use"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a global domain and no declared one serves globally, and says so", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := requirePreviewDomain(bare, global, nil, "pr-1", &out); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		for _, want := range []string{"global preview domain *.previews.ocel.dev", "declares no domains.preview"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to contain %q", out.String(), want)
			}
		}
	})

	t.Run("the slug prefix pushing a global label past 63 characters refuses", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Slug: "acme", Apps: []projectconfig.App{{Name: "admin"}, {Name: "web"}}}
		var out bytes.Buffer
		err := requirePreviewDomain(cfg, global, nil, strings.Repeat("b", 60), &out)
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a refusal")
		}
		for _, want := range []string{"DNS labels cap at 63", `project "acme" (4)`, "10 over"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a single-app project with no apps array still has its global label capped", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Slug: "acme"}
		pointer := strings.Repeat("b", 63)
		var out bytes.Buffer
		err := requirePreviewDomain(cfg, global, nil, pointer, &out)
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a refusal")
		}
		for _, want := range []string{"acme--" + pointer + " is 69 characters", "DNS labels cap at 63", `project "acme" (4)`, "6 over"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("an app-level preview domain counts as declared", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{
			Slug: "acme",
			Apps: []projectconfig.App{{Name: "web", Domains: map[string][]string{"preview": {"*.preview.acme.com"}}}},
		}
		broken := &contractv1.PreviewWildcard{BaseDomain: "previews.ocel.dev", GrammarMin: 1, GrammarMax: 1}
		var out bytes.Buffer
		if err := requirePreviewDomain(cfg, broken, nil, "pr-1", &out); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		if !strings.Contains(out.String(), "*.preview.acme.com") {
			t.Errorf("out = %q, want it to name the project's own preview domain", out.String())
		}
	})

	t.Run("the same label fits without the slug prefix on a declared domain", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{
			Slug:    "acme",
			Apps:    []projectconfig.App{{Name: "admin"}, {Name: "web"}},
			Domains: map[string][]string{"preview": {"*.preview.acme.com"}},
		}
		var out bytes.Buffer
		if err := requirePreviewDomain(cfg, nil, nil, strings.Repeat("b", 55), &out); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
	})

	t.Run("a declared domain and no global one is unchanged and silent", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := requirePreviewDomain(declared, nil, nil, "pr-1", &out); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		if out.String() != "" {
			t.Errorf("out = %q, want nothing said", out.String())
		}
	})

	t.Run("a declared domain wins over a global one, which is named as ignored", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := requirePreviewDomain(declared, global, nil, "pr-1", &out); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		for _, want := range []string{"*.preview.acme.com", "*.previews.ocel.dev", "ignored"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to contain %q", out.String(), want)
			}
		}
	})

	t.Run("an edge account mismatch refuses with the account to point at", func(t *testing.T) {
		t.Parallel()

		elsewhere := &contractv1.PreviewWildcard{BaseDomain: "previews.ocel.dev", EdgeScope: "cf-owner", GrammarMin: 1, GrammarMax: 1, RouteInstalled: true}
		var out bytes.Buffer
		err := requirePreviewDomain(bare, elsewhere, &contractv1.Identity{EdgeScope: "cf-other"}, "pr-1", &out)
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want an account refusal")
		}
		for _, want := range []string{"cf-owner", "cf-other", "edge account"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a missing wildcard route refuses, pointing at ocel domain use", func(t *testing.T) {
		t.Parallel()

		uninstalled := &contractv1.PreviewWildcard{BaseDomain: "previews.ocel.dev", GrammarMin: 1, GrammarMax: 1}
		var out bytes.Buffer
		err := requirePreviewDomain(bare, uninstalled, nil, "pr-1", &out)
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a route refusal")
		}
		for _, want := range []string{"wildcard route is not installed", "ocel domain use '*.previews.ocel.dev' --preview"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a grammar outside the installed worker's range refuses", func(t *testing.T) {
		t.Parallel()

		for _, g := range []*contractv1.PreviewWildcard{
			{BaseDomain: "previews.ocel.dev", GrammarMin: 2, GrammarMax: 3, RouteInstalled: true},
			{BaseDomain: "previews.ocel.dev", GrammarMin: 0, GrammarMax: 0, RouteInstalled: true},
		} {
			var out bytes.Buffer
			err := requirePreviewDomain(bare, g, nil, "pr-1", &out)
			if err == nil {
				t.Fatalf("requirePreviewDomain with grammar %d–%d = nil, want a refusal", g.GetGrammarMin(), g.GetGrammarMax())
			}
			if !strings.Contains(err.Error(), "ocel domain use") {
				t.Errorf("err = %v, want it to point at `ocel domain use`", err)
			}
		}
	})
}
