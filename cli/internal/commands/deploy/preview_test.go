package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
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
	const owner = variablestore.OwnerOcel
	binding := publishedPostgres("db--main")
	pair, err := variablestoreserver.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierPreview}
	if _, err := valueStore(fixture).SetBindings(context.Background(), scope, env.GetIdentity(), owner,
		[]variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
		t.Fatalf("publish %s to %s: %v", binding.GetName(), env.GetIdentity(), err)
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

func TestPreviewUpSendsThePreviewEnvironmentItsFlagsName(t *testing.T) {
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

	t.Run("--ref provisions the explicit ref's ephemeral", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("some-other-branch", ""), previewUpOptions{ref: "release/v2"})
		assertEnvironment(t, sentDeploy(t, fixture).GetEnvironment(), environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, previewKey(t, "release/v2"))
	})

	t.Run("--ref needs no git", func(t *testing.T) {
		dependencies := newTestDependencies()
		dependencies.ReadGitBranch = func(string) (string, error) { return "", errNotARepo }

		env, err := resolveUpEnvironment(dependencies, "", previewUpOptions{ref: "/tmp/some-fixture"})
		if err != nil {
			t.Fatalf("resolveUpEnvironment(--ref) err = %v, want it to resolve without git", err)
		}
		if want := previewKey(t, "/tmp/some-fixture"); env.GetIdentity() != want {
			t.Errorf("identity = %q, want %q", env.GetIdentity(), want)
		}
		if env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
			t.Errorf("lifecycle = %v, want ephemeral", env.GetLifecycle())
		}
	})

	t.Run("a persistent --name sends a persistent, declared environment", func(t *testing.T) {
		fixture := setUpPreviewProject(t)

		previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging"})
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

		err := runPreviewUp(context.Background(), newTestDependencies(), root, previewUpOptions{name: "staging"}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
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
	wildcard := stackrecords.Wildcard{BaseDomain: base, Edge: fake.KindRelay, GrammarMin: 1, GrammarMax: 1}
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

func TestPreviewRemoveDestroysThePreviewItsFlagsName(t *testing.T) {
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

	t.Run("--ref destroys the explicit ref", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("some-other-branch", "")
		previewUp(t, fixture, dependencies, previewUpOptions{ref: "release/v2"})

		previewRemove(t, fixture, dependencies, previewRemoveOptions{ref: "release/v2"})
		removed := removedEnvironments(t, fixture)
		if len(removed) != 1 {
			t.Fatalf("the CLI removed %d environments, want 1", len(removed))
		}
		assertEnvironment(t, removed[0], environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, previewKey(t, "release/v2"))
	})

	t.Run("a persistent preview with --yes is destroyed without prompting", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		dependencies := previewDependencies("feature/login", "")
		previewUp(t, fixture, dependencies, previewUpOptions{name: "staging"})

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

func TestTearingDownANamedPreviewAsksThroughConsentWhileTheRunIsHeld(t *testing.T) {
	fixture := setUpPreviewProject(t)
	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging"})
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	useJSONLogFormat(t, &dependencies)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, previewRemoveOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runPreviewRemove err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), `Tear down the named preview "staging"?`) {
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

func TestPreviewPruneReclaimsThePreviewItsFlagsName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		branch    string
		up        previewUpOptions
		prune     previewPruneOptions
		lifecycle environmentv1.Lifecycle
		identity  func(t *testing.T) string
	}{
		{
			name:      "with no flags it prunes the current branch's preview",
			branch:    "feature/login",
			lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			identity:  func(t *testing.T) string { return previewKey(t, "feature/login") },
		},
		{
			name:      "--ref prunes the explicit ref",
			branch:    "some-other-branch",
			up:        previewUpOptions{ref: "release/v2"},
			prune:     previewPruneOptions{ref: "release/v2"},
			lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			identity:  func(t *testing.T) string { return previewKey(t, "release/v2") },
		},
		{
			name:      "--name prunes the named preview",
			branch:    "feature/login",
			up:        previewUpOptions{name: "staging"},
			prune:     previewPruneOptions{name: "staging"},
			lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
			identity:  func(*testing.T) string { return "staging" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			dependencies := previewDependencies(tc.branch, "")
			previewUp(t, fixture, dependencies, tc.up)

			previewPrune(t, fixture, dependencies, tc.prune)
			pruned := prunedEnvironments(t, fixture)
			if len(pruned) != 1 {
				t.Fatalf("the CLI pruned %d environments, want 1", len(pruned))
			}
			assertEnvironment(t, pruned[0], tc.lifecycle, tc.identity(t))
		})
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
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}

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
	if !strings.Contains(stdout.String(), identity) || strings.Contains(stderr.String(), identity) {
		t.Errorf("stdout = %q, stream = %q: want the listing on stdout and not on the stream", stdout.String(), stderr.String())
	}
}

func TestPreviewListRendersEveryEnvironment(t *testing.T) {
	fixture := setUpPreviewProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{})
	previewUp(t, fixture, previewAppDependencies("feature/checkout", "7"), previewUpOptions{})
	previewUp(t, fixture, previewAppDependencies("feature/login", ""), previewUpOptions{name: "staging"})

	var stdout, stderr bytes.Buffer
	if err := runPreviewList(context.Background(), newTestDependencies(), fixture.Root, &stdout); err != nil {
		t.Fatalf("runPreviewList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{
		previewKey(t, "feature/login"), "ephemeral", "pr-7",
		"staging", "persistent", "—",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("stdout = %q, want it to contain %q", out, sub)
		}
	}
	if strings.Contains(out, "expires") {
		t.Errorf("stdout = %q, and a listing that prints an expiry promises a teardown nothing performs: no reaper, no sweep, and on an agentless box nothing resident that could run one", out)
	}
}

func TestAPreviewIsNamedByOneFlagThatFitsASubdomainLabel(t *testing.T) {
	t.Parallel()

	t.Run("--name and --ref are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name    string
			resolve func() error
		}{
			{"up", func() error {
				_, err := resolveUpEnvironment(newTestDependencies(), "", previewUpOptions{name: "staging", ref: "release/v2"})
				return err
			}},
			{"rm and prune", func() error {
				_, err := resolvePreviewEnvironment(newTestDependencies(), "", "staging", "release/v2")
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
				if !strings.Contains(err.Error(), "not both") {
					t.Errorf("err = %v, want it to say --name and --ref cannot be passed together", err)
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
				_, err := resolveUpEnvironment(newTestDependencies(), "", previewUpOptions{name: n})
				return err
			}},
			{"rm and prune", func(n string) error {
				_, err := resolvePreviewEnvironment(newTestDependencies(), "", n, "")
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
			previewUp(t, fixture, dependencies, previewUpOptions{name: "staging"})
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

func TestAPreviewNeedsADomainWhoseLabelsFit(t *testing.T) {
	t.Parallel()

	declared := &project.Project{Domains: project.Domains{Preview: "*.preview.acme.com"}}
	bare := &project.Project{}
	global := &contractv1.PreviewWildcard{
		BaseDomain:     "preview.ocel.app",
		GrammarMin:     1,
		GrammarMax:     1,
		RouteInstalled: true,
	}

	t.Run("neither a global domain nor a declared one refuses", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		_, err := requirePreviewDomain(bare, nil, nil, "pr-1", checkSpan(t, &out))
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
		if _, err := requirePreviewDomain(bare, global, nil, "pr-1", checkSpan(t, &out)); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		for _, want := range []string{"Serving previews on global *.preview.ocel.app"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to contain %q", out.String(), want)
			}
		}
	})

	t.Run("the slug prefix pushing a global label past 63 characters refuses", func(t *testing.T) {
		t.Parallel()

		cfg := &project.Project{Slug: "acme", Apps: []project.App{{Name: "admin"}, {Name: "web"}}}
		var out bytes.Buffer
		_, err := requirePreviewDomain(cfg, global, nil, strings.Repeat("b", 60), checkSpan(t, &out))
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

		cfg := &project.Project{Slug: "acme"}
		pointer := strings.Repeat("b", 63)
		var out bytes.Buffer
		_, err := requirePreviewDomain(cfg, global, nil, pointer, checkSpan(t, &out))
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a refusal")
		}
		for _, want := range []string{"acme--" + pointer + " is 69 characters", "DNS labels cap at 63", `project "acme" (4)`, "6 over"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("the same label fits without the slug prefix on a declared domain", func(t *testing.T) {
		t.Parallel()

		cfg := &project.Project{
			Slug:    "acme",
			Apps:    []project.App{{Name: "admin"}, {Name: "web"}},
			Domains: project.Domains{Preview: "*.preview.acme.com"},
		}
		var out bytes.Buffer
		if _, err := requirePreviewDomain(cfg, nil, nil, strings.Repeat("b", 55), checkSpan(t, &out)); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
	})

	t.Run("a declared domain and no global one is unchanged and silent", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if _, err := requirePreviewDomain(declared, nil, nil, "pr-1", checkSpan(t, &out)); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		if out.String() != "" {
			t.Errorf("out = %q, want nothing said", out.String())
		}
	})

	t.Run("a declared domain wins over a global one, which is named as ignored", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if _, err := requirePreviewDomain(declared, global, nil, "pr-1", checkSpan(t, &out)); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
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
		if _, err := requirePreviewDomain(same, global, nil, "pr-1", checkSpan(t, &out)); err != nil {
			t.Fatalf("requirePreviewDomain err = %v, want nil", err)
		}
		if got := out.String(); !strings.Contains(got, "Serving previews on project-level *.preview.ocel.app, also the global preview domain") || strings.Contains(got, "ignored") {
			t.Errorf("out = %q, want the one wildcard named as both, nothing ignored", got)
		}

		var over bytes.Buffer
		_, err := requirePreviewDomain(same, global, nil, strings.Repeat("b", 60), checkSpan(t, &over))
		if err != nil {
			t.Errorf("err = %v, want a 60-character label admitted: the hostnames are the project's own, so no slug segment counts against the cap", err)
		}
	})

	t.Run("an edge account mismatch refuses with the account to point at", func(t *testing.T) {
		t.Parallel()

		elsewhere := &contractv1.PreviewWildcard{BaseDomain: "preview.ocel.app", EdgeScope: "edge-owner", GrammarMin: 1, GrammarMax: 1, RouteInstalled: true}
		var out bytes.Buffer
		_, err := requirePreviewDomain(bare, elsewhere, &contractv1.Identity{EdgeScope: "edge-other"}, "pr-1", checkSpan(t, &out))
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want an account refusal")
		}
		for _, want := range []string{"edge-owner", "edge-other", "edge account"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a missing wildcard route refuses, pointing at ocel domain use", func(t *testing.T) {
		t.Parallel()

		uninstalled := &contractv1.PreviewWildcard{BaseDomain: "preview.ocel.app", GrammarMin: 1, GrammarMax: 1}
		var out bytes.Buffer
		_, err := requirePreviewDomain(bare, uninstalled, nil, "pr-1", checkSpan(t, &out))
		if err == nil {
			t.Fatal("requirePreviewDomain err = nil, want a route refusal")
		}
		for _, want := range []string{"wildcard route is not installed", "ocel domain use '*.preview.ocel.app' --preview"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a grammar outside the installed worker's range refuses", func(t *testing.T) {
		t.Parallel()

		for _, g := range []*contractv1.PreviewWildcard{
			{BaseDomain: "preview.ocel.app", GrammarMin: 2, GrammarMax: 3, RouteInstalled: true},
			{BaseDomain: "preview.ocel.app", GrammarMin: 0, GrammarMax: 0, RouteInstalled: true},
		} {
			var out bytes.Buffer
			_, err := requirePreviewDomain(bare, g, nil, "pr-1", checkSpan(t, &out))
			if err == nil {
				t.Fatalf("requirePreviewDomain with grammar %d–%d = nil, want a refusal", g.GetGrammarMin(), g.GetGrammarMax())
			}
			if !strings.Contains(err.Error(), "ocel domain use") {
				t.Errorf("err = %v, want it to point at `ocel domain use`", err)
			}
		}
	})
}
