package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/processenv"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

var errNotARepo = errors.New("determine current git branch: not a git repository")

func previewDependencies(branch, pr string) Dependencies {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	stubGit(&dependencies, branch, pr)
	return dependencies
}

func previewUp(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewUpOptions) string {
	t.Helper()
	coverEphemeralPreview(t, fixture, dependencies, opts)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func coverEphemeralPreview(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewUpOptions) {
	t.Helper()
	env, err := resolveUpEnvironment(dependencies, fixture.Root, opts)
	if err != nil {
		t.Fatalf("resolveUpEnvironment: %v", err)
	}
	if env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		return
	}
	coverPreview(t, fixture, env.GetIdentity())
}

func coverPreview(t *testing.T, fixture clitest.FakeProject, identity string) {
	t.Helper()
	const owner = variablestore.OwnerOcel
	binding := publishedPostgres("db--main")
	pair, err := variablestoreserver.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierPreview}
	if _, err := valueStore(fixture).SetBindings(context.Background(), scope, identity, owner,
		[]variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
		t.Fatalf("publish %s to %s: %v", binding.GetName(), identity, err)
	}
}

func previewKey(t *testing.T, ref string) string {
	t.Helper()
	id, err := previewid.Resolve(ref, "")
	if err != nil {
		t.Fatalf("previewid.Resolve: %v", err)
	}
	return id.Key
}

func assertEnvironment(t *testing.T, env *environmentv1.Environment, lifecycle environmentv1.Lifecycle, identity string) {
	t.Helper()
	if env.GetTier() != environmentv1.Tier_TIER_PREVIEW || env.GetLifecycle() != lifecycle || env.GetIdentity() != identity {
		t.Errorf("environment = %v, want a %s preview named %q", env, lifecycle, identity)
	}
}

func TestAPreviewBuildIsGivenTheAliasItsDeployIsServedOn(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	identity := previewKey(t, "feature/login")
	dependencies := previewDependencies("feature/login", "")
	stubBuild(&dependencies, apiFunction())

	previewUp(t, fixture, dependencies, previewUpOptions{})
	built := manifestVariable(t, sentDeploy(t, fixture).GetManifest(), "api", processenv.AppURLEnvVar).GetValue()
	if !regexp.MustCompile(`^https://` + regexp.QuoteMeta(identity) + `-[a-z2-7]{24}\.preview\.acme\.com$`).MatchString(built) {
		t.Fatalf("the build was given %q, want the preview's name and a signed token under *.preview.acme.com", built)
	}

	record := readDeployReport(t, fixture.Root)
	if len(record.GetApps()) != 1 || !slices.Contains(record.GetApps()[0].GetUrls(), built) {
		t.Errorf("the deploy recorded %v, want it served on %s, the alias its build was given", record.GetApps(), built)
	}
}

func TestAPreviewDeployCarriesTheAliasItsBuildWasGivenAndNotTheOneThisRunMinted(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	identity := previewKey(t, "feature/login")
	if _, err := stackrecords.EnsureAliasToken(context.Background(), fixture.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, identity, "abcdefghijklmnop"); err != nil {
		t.Fatal(err)
	}
	dependencies := previewDependencies("feature/login", "")
	stubBuild(&dependencies, apiFunction())

	previewUp(t, fixture, dependencies, previewUpOptions{})

	sent := sentDeploy(t, fixture)
	built := manifestVariable(t, sent.GetManifest(), "api", processenv.AppURLEnvVar).GetValue()
	if !strings.HasPrefix(built, "https://"+identity+"-abcdefghijklmnop") || sent.GetAliasToken() != "abcdefghijklmnop" {
		t.Errorf("the build was given %q and the deploy carried the alias %q, want both on abcdefghijklmnop, the alias another run recorded first", built, sent.GetAliasToken())
	}
}

func TestPreviewUpSendsThePreviewEnvironmentItsNameAndFlagsDescribe(t *testing.T) {
	t.Run("an ephemeral preview sends a preview, ephemeral environment", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		want := previewKey(t, "feature/login")

		out := previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{})
		assertEnvironment(t, sentDeploy(t, fixture).GetEnvironment(), environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, want)
		if !strings.Contains(out, "Deployed "+clitest.FixtureSlug+" to preview "+want) {
			t.Errorf("stdout = %q, want it to name the preview it deployed", out)
		}
	})

	t.Run("an app's functions reach the preview manifest", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		addAppToFixtureConfig(t, fixture.Root)
		dependencies := previewDependencies("feature/login", "")
		stubBuild(&dependencies, apiFunction())

		previewUp(t, fixture, dependencies, previewUpOptions{})
		functions := manifestApp(t, sentDeploy(t, fixture).GetManifest(), "api").GetServerless().GetFunctions()
		if len(functions) != 1 || functions[0].GetLogicalName() != "fn--api--api" || functions[0].GetArtifactPath() != "output/api" {
			t.Errorf("api functions = %v, want the function to have reached the preview manifest", functions)
		}
	})

	t.Run("a name needs no git", func(t *testing.T) {
		dependencies := newTestDependencies()
		dependencies.ReadGitBranch = func(string) (string, error) { return "", errNotARepo }

		env, err := resolveUpEnvironment(dependencies, "", previewUpOptions{name: "some-fixture"})
		if err != nil {
			t.Fatalf("resolveUpEnvironment(some-fixture) err = %v, want it to resolve without git", err)
		}
		assertEnvironment(t, env, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, "some-fixture")
	})

	t.Run("--persistent without a name makes the current branch's preview persistent", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{persistent: true})
		assertEnvironment(t, sentDeploy(t, fixture).GetEnvironment(), environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, previewKey(t, "feature/login"))
	})

	t.Run("a persistent name sends a persistent environment of that name", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
		assertEnvironment(t, sentDeploy(t, fixture).GetEnvironment(), environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, "staging")
	})

	t.Run("it declares the slug and the preview wildcard", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{})
		preflight := onlyPreflight(t, fixture)
		if preflight.GetSlug() != "test-app" || !slices.Equal(preflight.GetDomains(), []string{"*.preview.acme.com"}) || preflight.GetRequiredTier() != environmentv1.Tier_TIER_PREVIEW {
			t.Errorf("the preflight named slug %q, domains %v, tier %s; want the slug and the preview wildcard under the preview tier", preflight.GetSlug(), preflight.GetDomains(), preflight.GetRequiredTier())
		}
	})

	t.Run("it refuses without a preview domain, before anything is built", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
};
`)

		var stdout, stderr bytes.Buffer
		dependencies := previewDependencies("feature/login", "")
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-preview-domain refusal")
		}
		out := stdout.String()
		for _, want := range []string{"declares no preview domain", "domains.preview", "*.preview.", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "[build]") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("a project with no preview domain serves on the bootstrap's global one", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		useGlobalPreviewDomain(t, fixture, "preview.ocel.app")
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
};
`)

		out := previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{})
		if !strings.Contains(out, "Serving previews on global *.preview.ocel.app") {
			t.Errorf("stdout = %q, want it to name the global preview domain it serves on", out)
		}
		if env := sentDeploy(t, fixture).GetEnvironment(); env.GetTier() != environmentv1.Tier_TIER_PREVIEW {
			t.Errorf("deploy environment = %v, want a preview", env)
		}
	})

	t.Run("a tier mismatch refuses and drives no Deploy", func(t *testing.T) {
		fixture := setUpDeployProject(t)

		var stdout, stderr bytes.Buffer
		dependencies := previewDependencies("feature/login", "")
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runPreviewUp err = nil, want a tier-mismatch error")
		}
		if !strings.Contains(stdout.String(), "this command needs preview infrastructure") {
			t.Errorf("stdout = %q, want the concrete tier-mismatch message", stdout.String())
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("absent infrastructure refuses and drives no Deploy", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		removeBootstrap(t, fixture, environment.TierProduction)

		var stdout, stderr bytes.Buffer
		dependencies := previewDependencies("feature/login", "")
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runPreviewUp err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(stdout.String(), "ocel bootstrap preview") {
			t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap preview`", stdout.String())
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("a slug this preview environment has never seen is guarded like production's", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		recordProjects(t, fixture, environment.TierPreview, "my-application", "billing")
		dependencies := previewDependencies("feature/login", "")
		terminalStdin(&dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"This will create a NEW project.", "This backend already has: billing, my-application", "Not confirmed, so this run changes nothing", "Nothing deployed to preview"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want the declined guard to stop the preview before it deploys", len(sent))
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runPreviewUp(context.Background(), newTestDependencies(), root, previewUpOptions{name: "staging", persistent: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})
}

func useGlobalPreviewDomain(t *testing.T, fixture clitest.FakeProject, base string) {
	t.Helper()
	wildcard := stackrecords.Wildcard{BaseDomain: base, Edge: fake.KindRelay}
	encoded, err := json.Marshal(wildcard)
	if err != nil {
		t.Fatal(err)
	}
	name := stackrecords.WildcardKey(environment.TierPreview)
	entry, err := keyvalue.ReadOrEmpty(context.Background(), fixture.Provider.KeyValues(), name)
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = encoded
	if _, err := fixture.Provider.KeyValues().Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	relayEdge(fixture).Owns(wildcard.Hostname(), edge.PreviewEntryOwner)
}

func TestRunDeployWithoutAPreviewDomain(t *testing.T) {
	t.Run("a production deploy needs no preview domain", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "acme.com" },
};
`)
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	})
}

func removedEnvironments(t *testing.T, fixture clitest.FakeProject) []*environmentv1.Environment {
	t.Helper()
	var removed []*environmentv1.Environment
	for _, req := range clitest.RequestsTo[*contractv1.RemoveEnvironmentRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveEnvironmentProcedure) {
		if req.GetSlug() != clitest.FixtureSlug {
			t.Errorf("removed an environment of %q, want %s's", req.GetSlug(), clitest.FixtureSlug)
		}
		removed = append(removed, req.GetEnvironment())
	}
	return removed
}

func previewRemove(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewRemoveOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestPreviewRemoveTearsDownThePreviewItsNameNames(t *testing.T) {
	t.Run("an ephemeral preview for the current branch is destroyed without prompting", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("feature/login", "")
		previewUp(t, fixture, dependencies, previewUpOptions{})

		out := previewRemove(t, fixture, dependencies, previewRemoveOptions{})
		removed := removedEnvironments(t, fixture)
		if len(removed) != 1 {
			t.Fatalf("the CLI removed %d environments, want 1", len(removed))
		}
		assertEnvironment(t, removed[0], environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, previewKey(t, "feature/login"))
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want no prompt for ephemeral teardown", out)
		}
	})

	t.Run("rm <name> tears down the ephemeral preview of that name without asking", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("some-other-branch", "")
		previewUp(t, fixture, dependencies, previewUpOptions{name: "release-v2"})

		out, err := runPreviewCommand(t, fixture, dependencies, "", "rm", "release-v2")
		if err != nil {
			t.Fatalf("ocel preview rm release-v2 err = %v; out=%s", err, out)
		}
		removed := removedEnvironments(t, fixture)
		if len(removed) != 1 {
			t.Fatalf("the CLI removed %d environments, want 1", len(removed))
		}
		assertEnvironment(t, removed[0], environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, "release-v2")
	})

	t.Run("a preview nothing was ever deployed to is torn down without asking", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("feature/login", "")
		previewUp(t, fixture, dependencies, previewUpOptions{})
		terminalStdin(&dependencies)

		out, err := runPreviewCommand(t, fixture, dependencies, "", "rm", "never-deployed")
		if err != nil {
			t.Fatalf("ocel preview rm never-deployed err = %v; out=%s", err, out)
		}
		if removed := removedEnvironments(t, fixture); len(removed) != 1 || removed[0].GetIdentity() != "never-deployed" {
			t.Errorf("the CLI removed %v, want never-deployed reclaimed", removed)
		}
	})

	t.Run("a preview whose record names no lifecycle is torn down unattended without asking", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("feature/login", "")
		previewUp(t, fixture, dependencies, previewUpOptions{})
		if _, err := stackrecords.EnsureAliasToken(context.Background(), fixture.Provider.KeyValues(),
			environment.TierPreview, clitest.FixtureSlug, "staging", "aaaaaaaaaaaaaaaa"); err != nil {
			t.Fatal(err)
		}

		out, err := runPreviewCommand(t, fixture, dependencies, "", "rm", "staging")
		if err != nil {
			t.Fatalf("ocel preview rm staging err = %v; out=%s", err, out)
		}
		if removed := removedEnvironments(t, fixture); len(removed) != 1 || removed[0].GetIdentity() != "staging" {
			t.Errorf("the CLI removed %v, want staging reclaimed: a record without a lifecycle was never deployed", removed)
		}
	})

	t.Run("a persistent preview with --yes is destroyed without prompting", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("feature/login", "")
		previewUp(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})

		out := previewRemove(t, fixture, dependencies, previewRemoveOptions{name: "staging", yes: true})
		removed := removedEnvironments(t, fixture)
		if len(removed) != 1 {
			t.Fatalf("the CLI removed %d environments, want 1", len(removed))
		}
		assertEnvironment(t, removed[0], environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, "staging")
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to skip the prompt", out)
		}
	})
}

func TestAPersistentPreviewIsNotTornDownUnasked(t *testing.T) {
	for _, tc := range []struct {
		name     string
		create   func(t *testing.T, fixture clitest.FakeProject)
		question string
	}{
		{"a persistent preview, though rm names no lifecycle", func(t *testing.T, fixture clitest.FakeProject) {
			previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
		}, `Tear down the persistent preview "staging"?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			tc.create(t, fixture)
			dependencies := previewDependencies("feature/login", "")
			terminalStdin(&dependencies)

			out, err := runPreviewCommand(t, fixture, dependencies, "n\n", "rm", "staging")
			if err != nil {
				t.Fatalf("ocel preview rm staging err = %v; out=%s", err, out)
			}
			if !strings.Contains(out, tc.question) {
				t.Errorf("out = %q, want it to ask %q", out, tc.question)
			}
			if removed := removedEnvironments(t, fixture); len(removed) != 0 {
				t.Errorf("the CLI removed %v, want nothing torn down once the question is declined", removed)
			}
		})

		t.Run(tc.name+", unattended without --yes, is refused", func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			tc.create(t, fixture)
			dependencies := previewDependencies("feature/login", "")
			var stream bytes.Buffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stream))

			out, err := runPreviewCommand(t, fixture, dependencies, "", "rm", "staging")
			if err == nil || !strings.Contains(out, "to run it unattended, pass --yes") {
				t.Errorf("ocel preview rm staging err = %v, want a refusal naming --yes; out=%s", err, out)
			}
			evs := clitest.RunEvents(t, stream.String())
			if got := evs[len(evs)-1].GetSummary().GetError(); got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
				t.Errorf("summary error = %v, want code confirmation_required with the hint --yes", got)
			}
			if removed := removedEnvironments(t, fixture); len(removed) != 0 {
				t.Errorf("the CLI removed %v, want nothing torn down without a terminal to ask on", removed)
			}
		})

		t.Run(tc.name+", under --json on a terminal, is refused and nothing enters the stream", func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			tc.create(t, fixture)
			dependencies := previewDependencies("feature/login", "")
			tty, screen := clitest.UnderJSONOnATerminal(t, &dependencies.Invocation)
			var stdout bytes.Buffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stdout))

			err := clitest.FinishWithin(t, 20*time.Second, func(ctx context.Context) error {
				return runPreviewRemove(ctx, dependencies, fixture.Root, previewRemoveOptions{name: "staging"}, &stdout, &stdout, tty)
			})

			if err == nil {
				t.Error("runPreviewRemove err = nil, want a refusal")
			}
			if asked := screen(); asked != "" {
				t.Errorf("terminal = %q, want nothing asked under --json", asked)
			}
			for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
				if !json.Valid([]byte(line)) {
					t.Errorf("stdout line %q is not JSON, want only the JSON stream", line)
				}
			}
			evs := clitest.RunEvents(t, stdout.String())
			got := evs[len(evs)-1].GetSummary().GetError()
			if got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
				t.Errorf("summary error = %v, want code confirmation_required with the hint --yes", got)
			}
			if !strings.Contains(got.GetMessage(), "needs a terminal, without --json,") {
				t.Errorf("summary error message = %q, want a refusal true under --json on a terminal", got.GetMessage())
			}
			if removed := removedEnvironments(t, fixture); len(removed) != 0 {
				t.Errorf("the CLI removed %v, want nothing torn down under --json", removed)
			}
		})
	}
}

func TestTearingDownAPersistentPreviewAsksThroughConsentWhileTheRunIsHeld(t *testing.T) {
	fixture := setUpPreviewProject(t)
	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, previewRemoveOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runPreviewRemove err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), `Tear down the persistent preview "staging"?`) {
		t.Errorf("stdout = %q, want the teardown asked about by name", stdout.String())
	}
	evs := envelopes(t, stream.String())
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < 0 {
		t.Fatalf("the run was never held while it asked: %s", stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" || !bytes.Equal(evs[resumed].GetOperation().GetSpanId(), evs[waiting].GetOperation().GetSpanId()) {
		t.Fatalf("resumed at event %d, waiting at %d: want the held span resumed once answered: %s", resumed, waiting, stream.String())
	}
	destroyed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetPhase() == progressv1.Phase_PHASE_DESTROY
	})
	if destroyed < resumed {
		t.Errorf("teardown at event %d, resumed at %d: want nothing torn down until the question is answered: %s", destroyed, resumed, stream.String())
	}
	result := evs[len(evs)-1].GetSummary()
	if !result.GetSuccess() || result.GetHeadline() != "Tore down preview staging of "+clitest.FixtureSlug {
		t.Errorf("result = %v, want the run to end reporting the preview torn down", result)
	}
}

func prunedEnvironments(t *testing.T, fixture clitest.FakeProject) []*environmentv1.Environment {
	t.Helper()
	var pruned []*environmentv1.Environment
	for _, req := range clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure) {
		if req.GetSlug() != clitest.FixtureSlug || req.GetKeepN() != defaultPreviewPruneKeepN {
			t.Errorf("pruned %q down to %d, want %s's down to %d", req.GetSlug(), req.GetKeepN(), clitest.FixtureSlug, defaultPreviewPruneKeepN)
		}
		pruned = append(pruned, req.GetEnvironment())
	}
	return pruned
}

func previewPrune(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewPruneOptions) {
	t.Helper()
	opts.keep = defaultPreviewPruneKeepN
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewPrune(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewPrune err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func TestPreviewPruneReclaimsThePreviewItsNameNames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		up       previewUpOptions
		prune    previewPruneOptions
		identity func(t *testing.T) string
	}{
		{
			name:     "with no name it prunes the current branch's preview",
			identity: func(t *testing.T) string { return previewKey(t, "feature/login") },
		},
		{
			name:     "a name prunes the ephemeral preview of that name",
			up:       previewUpOptions{name: "release-v2"},
			prune:    previewPruneOptions{name: "release-v2"},
			identity: func(*testing.T) string { return "release-v2" },
		},
		{
			name:     "a name prunes the persistent preview of that name",
			up:       previewUpOptions{name: "staging", persistent: true},
			prune:    previewPruneOptions{name: "staging"},
			identity: func(*testing.T) string { return "staging" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			dependencies := previewDependencies("feature/login", "")
			previewUp(t, fixture, dependencies, tc.up)

			previewPrune(t, fixture, dependencies, tc.prune)
			pruned := prunedEnvironments(t, fixture)
			if len(pruned) != 1 {
				t.Fatalf("the CLI pruned %d environments, want 1", len(pruned))
			}
			if pruned[0].GetTier() != environmentv1.Tier_TIER_PREVIEW || pruned[0].GetIdentity() != tc.identity(t) {
				t.Errorf("pruned %v, want the preview %q", pruned[0], tc.identity(t))
			}
		})
	}
}

func TestPreviewPruneTakesItsNameOnTheCommandLine(t *testing.T) {
	fixture := setUpPreviewProject(t)
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{name: "staging"})

	out, err := runPreviewCommand(t, fixture, dependencies, "", "prune", "staging", "--keep", "5")
	if err != nil {
		t.Fatalf("ocel preview prune staging err = %v; out=%s", err, out)
	}
	requests := clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure)
	if len(requests) != 1 || requests[0].GetEnvironment().GetIdentity() != "staging" || requests[0].GetKeepN() != 5 {
		t.Errorf("the CLI pruned %d environments, want staging pruned down to 5", len(requests))
	}
}

func previewAppDependencies(branch, pr string) Dependencies {
	dependencies := previewDependencies(branch, pr)
	stubBuild(&dependencies, apiFunction())
	return dependencies
}

func TestListingPreviewsStartsTheProviderInTheCheckPhaseOfItsRunAndPrintsTheListingAloneOnStdout(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{})
	identity := previewKey(t, "feature/login")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runPreviewList(context.Background(), dependencies, fixture.Root, &stdout); err != nil {
		t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := envelopes(t, stderr.String())
	opened := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetOperation().GetStarted() != nil })
	if opened < 0 || evs[opened].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("the listing's run never opened the check phase that starts the provider: %s", stderr.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() {
		t.Errorf("result = %v, want the listing's run to succeed", result)
	}
	var listed resultv1.PreviewListResult
	clitest.DecodeResultInto(t, stdout.String(), &listed)
	if len(listed.GetPreviews()) != 1 || listed.GetPreviews()[0].GetIdentity() != identity || strings.Contains(stderr.String(), identity) {
		t.Errorf("stdout = %q, stream = %q: want the listing as an envelope on stdout and not on the stream", stdout.String(), stderr.String())
	}
}

func TestPreviewListAsJSONCarriesEachPreviewsLifecycleLabelAndAliases(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{})
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runPreviewList(context.Background(), dependencies, fixture.Root, &stdout); err != nil {
		t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var listed resultv1.PreviewListResult
	clitest.DecodeResultInto(t, stdout.String(), &listed)
	byIdentity := map[string]*resultv1.PreviewSummary{}
	for _, preview := range listed.GetPreviews() {
		byIdentity[preview.GetIdentity()] = preview
	}
	staging, ephemeral := byIdentity["staging"], byIdentity[previewKey(t, "feature/login")]
	if staging == nil || ephemeral == nil {
		t.Fatalf("previews = %v, want staging and the branch's preview", listed.GetPreviews())
	}
	if staging.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT || ephemeral.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		t.Errorf("lifecycles = %v and %v, want persistent and ephemeral", staging.GetLifecycle(), ephemeral.GetLifecycle())
	}
	if _, err := time.Parse(time.RFC3339, staging.GetCreatedAt()); err != nil {
		t.Errorf("createdAt = %q, want an RFC 3339 timestamp: %v", staging.GetCreatedAt(), err)
	}
	if len(staging.GetAliasUrls()) == 0 || !strings.HasPrefix(staging.GetAliasUrls()[0], "https://staging-") {
		t.Errorf("aliasUrls = %v, want the preview's alias", staging.GetAliasUrls())
	}
}

func TestPreviewListAsJSONPrintsAnEmptyListWhenThereAreNoPreviews(t *testing.T) {
	fixture := setUpPreviewProject(t)
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runPreviewList(context.Background(), dependencies, fixture.Root, &stdout); err != nil {
		t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if data := clitest.DecodeResult(t, stdout.String()); !reflect.DeepEqual(data["previews"], []any{}) {
		t.Errorf("data = %v, want an empty previews list", data)
	}
}

func TestPreviewListRendersEveryEnvironment(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{})
	previewUp(t, fixture, previewAppDependencies("feature/checkout", "7"), previewUpOptions{})
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})

	var stdout, stderr bytes.Buffer
	if err := runPreviewList(context.Background(), newTestDependencies(), fixture.Root, &stdout); err != nil {
		t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{
		previewKey(t, "feature/login"), "ephemeral", "pr-7",
		"staging", "persistent", "—",
		"https://staging-", "https://" + previewKey(t, "feature/login") + "-",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("stdout = %q, want it to contain %q", out, sub)
		}
	}
	if strings.Contains(out, "expires") {
		t.Errorf("stdout = %q, and a listing that prints an expiry promises a teardown nothing performs: no reaper, no sweep, and on an agentless box nothing resident that could run one", out)
	}
}

func TestAPreviewNameFitsASubdomainLabel(t *testing.T) {
	t.Parallel()

	t.Run("a subcommand's name is a valid preview name", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"up", "rm", "ls", "prune"} {
			if _, err := resolvePreviewEnvironment(newTestDependencies(), "", name, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL); err != nil {
				t.Errorf("preview name %q refused: %v, want it accepted: `ocel preview up %s` cannot be read as a subcommand", name, err, name)
			}
		}
	})

	t.Run("a name with -- is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := resolvePreviewEnvironment(newTestDependencies(), "", "feature--login", environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL); err == nil {
			t.Error("preview name feature--login accepted, want it refused")
		}
	})

	t.Run("a preview name is capped for the subdomain label", func(t *testing.T) {
		t.Parallel()

		atCap := "a" + strings.Repeat("b", 62)
		overCap := atCap + "c"

		cases := []struct {
			name    string
			resolve func(string) error
		}{
			{"up", func(n string) error {
				_, err := resolveUpEnvironment(newTestDependencies(), "", previewUpOptions{name: n})
				return err
			}},
			{"rm and prune", func(n string) error {
				_, err := resolvePreviewEnvironment(newTestDependencies(), "", n, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED)
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				if err := tc.resolve(atCap); err != nil {
					t.Errorf("a name of %d chars rejected: %v", len(atCap), err)
				}
				err := tc.resolve(overCap)
				if err == nil {
					t.Fatalf("a name of %d chars accepted, want a rejection", len(overCap))
				}
				if !strings.Contains(err.Error(), "too long") {
					t.Errorf("err = %v, want it to say the name is too long", err)
				}
			})
		}
	})
}

func TestPreviewPreflightShapeKeepsTeardownOffTheSharedWildcardRefusal(t *testing.T) {
	const why = "the provider refuses a global-preview account mismatch only for a preflight that includes a slug and no domains, " +
		"because that is exactly a preview deploy landing on the shared wildcard; a teardown that starts sending a slug would be refused and strand its resources"

	t.Run("up sends the slug and the project's declared preview hostnames, so the shared-wildcard refusal can reach it", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{})
		got := onlyPreflight(t, fixture)
		if got.GetSlug() == "" {
			t.Errorf("`ocel preview up` sent an empty slug, want the project slug: %s", why)
		}
		if want := []string{"*.preview.acme.com"}; !slices.Equal(got.GetDomains(), want) {
			t.Errorf("`ocel preview up` sent domains %v, want the project's declared preview hostnames %v: %s", got.GetDomains(), want, why)
		}
	})

	teardowns := []struct {
		name string
		run  func(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies)
	}{
		{"rm", func(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies) {
			previewRemove(t, fixture, dependencies, previewRemoveOptions{name: "staging", yes: true})
		}},
		{"prune", func(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies) {
			previewPrune(t, fixture, dependencies, previewPruneOptions{name: "staging"})
		}},
	}
	for _, tc := range teardowns {
		t.Run(tc.name+" sends neither a slug nor domains, so the shared-wildcard refusal never reaches a teardown", func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			dependencies := previewDependencies("feature/login", "")
			previewUp(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})
			before := len(sentPreflights(t, fixture))

			tc.run(t, fixture, dependencies)
			got := sentPreflights(t, fixture)[before:]
			if len(got) != 1 {
				t.Fatalf("`ocel preview %s` issued %d preflights, want exactly 1", tc.name, len(got))
			}
			if got[0].GetSlug() != "" {
				t.Errorf("`ocel preview %s` sent slug %q, want none: %s", tc.name, got[0].GetSlug(), why)
			}
			if len(got[0].GetDomains()) != 0 {
				t.Errorf("`ocel preview %s` sent domains %v, want none: %s", tc.name, got[0].GetDomains(), why)
			}
		})
	}

	t.Run("ls preflights not at all, so nothing about it can be refused", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		var stdout, stderr bytes.Buffer
		if err := runPreviewList(context.Background(), newTestDependencies(), fixture.Root, &stdout); err != nil {
			t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if got := sentPreflights(t, fixture); len(got) != 0 {
			t.Errorf("`ocel preview ls` issued %d preflights, want none; %s", len(got), why)
		}
	})
}

func checkSpan(t *testing.T, w io.Writer) *run.Span {
	t.Helper()
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewSink(terminal.Presentation{}, w))
	_, run, err := bus.Begin(context.Background(), "ocel preview up", "")
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}
	return run.Phase(progressv1.Phase_PHASE_CHECK)
}

func TestAPreviewNeedsADomainToServeOn(t *testing.T) {
	t.Parallel()

	declared := &project.Project{Domains: project.Domains{Preview: "*.preview.acme.com"}}
	bare := &project.Project{}
	global := &contractv1.PreviewWildcard{
		BaseDomain:     "preview.ocel.app",
		RouteInstalled: true,
	}

	t.Run("neither a global domain nor a declared one refuses", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		err := refuseMissingPreviewDomain(bare, nil, nil, true, checkSpan(t, &out))
		if err == nil {
			t.Fatal("refuseMissingPreviewDomain err = nil, want a refusal")
		}
		for _, want := range []string{"declares no preview domain", "no global one", "domains.preview", "ocel domain use"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("an edge that gives each release its own address serves a preview with no domain, and says so", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := refuseMissingPreviewDomain(bare, nil, nil, false, checkSpan(t, &out)); err != nil {
			t.Fatalf("refuseMissingPreviewDomain err = %v, want nil: no hostname is needed where the router addresses each release", err)
		}
		if want := "Serving previews on the address each app's release is given"; !strings.Contains(out.String(), want) {
			t.Errorf("out = %q, want it to contain %q", out.String(), want)
		}
	})

	t.Run("a global domain and no declared one serves globally, and says so", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := refuseMissingPreviewDomain(bare, global, nil, true, checkSpan(t, &out)); err != nil {
			t.Fatalf("refuseMissingPreviewDomain err = %v, want nil", err)
		}
		for _, want := range []string{"Serving previews on global *.preview.ocel.app"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to contain %q", out.String(), want)
			}
		}
	})

	t.Run("a declared domain and no global one is unchanged and silent", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := refuseMissingPreviewDomain(declared, nil, nil, true, checkSpan(t, &out)); err != nil {
			t.Fatalf("refuseMissingPreviewDomain err = %v, want nil", err)
		}
		if out.String() != "" {
			t.Errorf("out = %q, want nothing said", out.String())
		}
	})

	t.Run("a declared domain wins over a global one, which is named as ignored", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if err := refuseMissingPreviewDomain(declared, global, nil, true, checkSpan(t, &out)); err != nil {
			t.Fatalf("refuseMissingPreviewDomain err = %v, want nil", err)
		}
		for _, want := range []string{"*.preview.acme.com", "*.preview.ocel.app", "ignored"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to contain %q", out.String(), want)
			}
		}
	})

	t.Run("a declared domain equal to the global one serves as the project's own and calls nothing ignored", func(t *testing.T) {
		t.Parallel()

		same := &project.Project{Slug: "acme", Domains: project.Domains{Preview: "*.preview.ocel.app"}}
		var out bytes.Buffer
		if err := refuseMissingPreviewDomain(same, global, nil, true, checkSpan(t, &out)); err != nil {
			t.Fatalf("refuseMissingPreviewDomain err = %v, want nil", err)
		}
		if got := out.String(); !strings.Contains(got, "Serving previews on project-level *.preview.ocel.app, also the global preview domain") || strings.Contains(got, "ignored") {
			t.Errorf("out = %q, want the one wildcard named as both, nothing ignored", got)
		}
	})

	t.Run("an edge account mismatch refuses with the account to point at", func(t *testing.T) {
		t.Parallel()

		elsewhere := &contractv1.PreviewWildcard{BaseDomain: "preview.ocel.app", EdgeScope: "edge-owner", RouteInstalled: true}
		var out bytes.Buffer
		err := refuseMissingPreviewDomain(bare, elsewhere, &contractv1.Identity{EdgeScope: "edge-other"}, true, checkSpan(t, &out))
		if err == nil {
			t.Fatal("refuseMissingPreviewDomain err = nil, want an account refusal")
		}
		for _, want := range []string{"edge-owner", "edge-other", "edge account"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a missing wildcard route refuses, pointing at ocel domain use", func(t *testing.T) {
		t.Parallel()

		uninstalled := &contractv1.PreviewWildcard{BaseDomain: "preview.ocel.app"}
		var out bytes.Buffer
		err := refuseMissingPreviewDomain(bare, uninstalled, nil, true, checkSpan(t, &out))
		if err == nil {
			t.Fatal("refuseMissingPreviewDomain err = nil, want a route refusal")
		}
		for _, want := range []string{"wildcard route is not installed", "ocel domain use '*.preview.ocel.app' --preview"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

}

func runPreviewCommand(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(fixture.Root)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	cmd := NewPreviewCommand(dependencies)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(stdin))
	err := cmd.ExecuteContext(context.Background())
	return stdout.String() + stderr.String(), err
}

func TestAPreviewNamedOnTheCommandLineIsEphemeralUnlessPersistent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		lifecycle environmentv1.Lifecycle
	}{
		{"preview up <name> deploys an ephemeral preview of that name", []string{"up", "staging"}, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL},
		{"preview up <name> --persistent deploys a persistent preview of that name", []string{"up", "staging", "--persistent"}, environmentv1.Lifecycle_LIFECYCLE_PERSISTENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			coverPreview(t, fixture, "staging")

			out, err := runPreviewCommand(t, fixture, previewDependencies("feature/login", ""), "", tc.args...)
			if err != nil {
				t.Fatalf("ocel preview %v err = %v; out=%s", tc.args, err, out)
			}
			assertEnvironment(t, sentDeploy(t, fixture).GetEnvironment(), tc.lifecycle, "staging")
		})
	}
}

func TestPreviewWithoutASubcommandTakesNoName(t *testing.T) {
	fixture := setUpPreviewProject(t)

	out, err := runPreviewCommand(t, fixture, previewDependencies("feature/login", ""), "", "staging")
	if err == nil {
		t.Fatalf("ocel preview staging err = nil, want it refused: a name goes to ocel preview up, so no name can read as a subcommand; out=%s", out)
	}
	if !strings.Contains(err.Error(), "ocel preview up staging") {
		t.Errorf("err = %v, want it to point at ocel preview up staging", err)
	}
}

func TestPreviewListShowsADashForAPreviewWithNoLifecycle(t *testing.T) {
	var out strings.Builder
	renderEnvironments(&out, []*contractv1.PreviewEnvironment{{Identity: "staging"}})

	if fields := strings.Split(out.String(), "\t"); len(fields) < 2 || fields[1] != "—" {
		t.Errorf("ocel preview ls printed %q, want a dash where the lifecycle goes: the provider named none", out.String())
	}
}

func TestAFirstPreviewUpThatBuildsNothingToDeployLeavesNoPreviewBehind(t *testing.T) {
	fixture := setUpPreviewProject(t)
	writeConfig(t, fixture.Root, "")
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(fixture.Root), "main.ts"), "export {};\n")
	dependencies := previewDependencies("feature/login", "")

	out := previewUp(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})
	if !strings.Contains(out, "Nothing to deploy") {
		t.Fatalf("stdout = %q, want nothing to deploy", out)
	}

	_, err := fixture.Provider.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, clitest.FixtureSlug, "staging"))
	if !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading staging's record after its first preview up had nothing to deploy = %v, want nothing recorded: `ocel preview ls` would list a preview that was never deployed", err)
	}
}

func TestAPersistentPreviewUpWhoseBuildFailsAfterItsInfraFreesThePreviewItHeld(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	dependencies := previewDependencies("feature/login", "")
	stubBuild(&dependencies, apiFunction())
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("simulated build failure")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{name: "staging", persistent: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runPreviewUp succeeded through a failed build")
	}

	if len(sentProvisionInfras(t, fixture)) != 1 {
		t.Fatal("the build failed before ProvisionInfra ran, so this test holds no lease to free")
	}
	_, err := fixture.Provider.KeyValues().Read(context.Background(), stackrecords.EnvironmentLeaseKey(environment.TierPreview, clitest.FixtureSlug, "staging"))
	if !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the lease of preview staging = %v, want none: a preview up whose build failed abandons the lease its infra took", err)
	}
}

func TestAFirstPreviewUpWhoseBuildFailsAfterItsInfraIsProvisionedLeavesThePreviewRecorded(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	dependencies := previewDependencies("feature/login", "")
	stubBuild(&dependencies, apiFunction())
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("simulated build failure")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{name: "staging", persistent: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runPreviewUp succeeded through a failed build")
	}

	if sent := sentProvisionInfras(t, fixture); len(sent) != 1 {
		t.Fatalf("the CLI sent %d ProvisionInfra requests, want the infra provisioned before the build failed", len(sent))
	}
	meta, err := stackrecords.ReadEnvironmentMeta(context.Background(), fixture.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Lifecycle != stackrecords.LifecyclePersistent {
		t.Errorf("staging records lifecycle %q after its first preview up failed to build, want persistent: its infra was provisioned, and `ocel preview rm` finds it through that record",
			meta.Lifecycle)
	}
}

func TestAPreviewAliasIsAssignedOnlyWhereAHostnameServesIt(t *testing.T) {
	t.Parallel()

	declared := &project.Project{Domains: project.Domains{Preview: "*.preview.acme.com"}}
	bare := &project.Project{}
	global := &contractv1.PreviewWildcard{BaseDomain: "preview.ocel.app"}

	for name, tc := range map[string]struct {
		cfg              *project.Project
		wildcard         *contractv1.PreviewWildcard
		hostnameRequired bool
		want             bool
	}{
		"a router that addresses each release and no domain has no hostname to assign":     {bare, nil, false, false},
		"a declared domain is assigned an alias even where the router addresses itself":    {declared, nil, false, true},
		"a global domain is assigned an alias even where the router addresses itself":      {bare, global, false, true},
		"a router that needs a hostname asks the provider, which refuses a missing domain": {bare, nil, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := assignsPreviewAlias(tc.cfg, tc.wildcard, tc.hostnameRequired); got != tc.want {
				t.Errorf("assignsPreviewAlias() = %v, want %v", got, tc.want)
			}
		})
	}
}

func removedWithRegistry(t *testing.T, fixture clitest.FakeProject) []*contractv1.ImageRegistry {
	t.Helper()
	var registries []*contractv1.ImageRegistry
	for _, req := range clitest.RequestsTo[*contractv1.RemoveEnvironmentRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveEnvironmentProcedure) {
		registries = append(registries, req.GetProjectRegistry())
	}
	return registries
}

func TestRemovingAPreviewSendsTheRegistryTheProjectNamesSoTheImagesItPushedGoWithIt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	fixture := setUpPreviewProject(t)
	writeConfig(t, fixture.Root, `  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`+"\n")
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{})

	previewRemove(t, fixture, dependencies, previewRemoveOptions{})

	registries := removedWithRegistry(t, fixture)
	if len(registries) != 1 || registries[0].GetServer() != "registry.example.com" || registries[0].GetUsername() != "acme-bot" || registries[0].GetPassword() != "hunter2" {
		t.Errorf("the removal named %d registries, want the project's registry with its secret resolved: it is how the images the preview pushed there are deleted", len(registries))
	}
}

func TestRemovingAPreviewWhoseRegistryVariableIsUnsetStillRemovesItAndSaysWhatItLeft(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "")
	fixture := setUpPreviewProject(t)
	writeConfig(t, fixture.Root, `  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`+"\n")
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{})

	out := previewRemove(t, fixture, dependencies, previewRemoveOptions{})

	if registries := removedWithRegistry(t, fixture); len(registries) != 1 || registries[0] != nil {
		t.Fatalf("the removal named %d registries, want one removal naming none: a token that is gone must not keep a preview from being torn down", len(registries))
	}
	for _, want := range []string{"OCEL_TEST_REGISTRY_TOKEN", "registry.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to say the images stay in the registry and name %q", out, want)
		}
	}
}

func TestPruningAPreviewSendsTheRegistryTheProjectNamesSoTheImagesOfWhatItReclaimsGoWithThem(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	fixture := setUpPreviewProject(t)
	writeConfig(t, fixture.Root, `  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`+"\n")
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{})

	previewPrune(t, fixture, dependencies, previewPruneOptions{})

	requests := clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure)
	if len(requests) != 1 || requests[0].GetProjectRegistry().GetServer() != "registry.example.com" || requests[0].GetProjectRegistry().GetPassword() != "hunter2" {
		t.Errorf("the prune sent %d requests, want one naming the project's registry with its secret resolved: it is how the images of the releases it reclaims are deleted", len(requests))
	}
}
