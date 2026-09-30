package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestADeployRefusesEveryHostnameAnotherProjectClaims(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		claims []*contractv1.DomainClaim
		refuse bool
	}{
		{
			name:   "no claims asked for",
			claims: nil,
		},
		{
			name:   "unclaimed passes",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_UNCLAIMED}},
		},
		{
			name:   "unanswerable is skipped, never refused",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_UNSPECIFIED}},
		},
		{
			name:   "claimed refuses",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-preview"}},
			refuse: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := refuseClaimedDomains(tc.claims, project.DefaultFileName, func(string) {})
			if !tc.refuse {
				if err != nil {
					t.Fatalf("refuseClaimedDomains err = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("refuseClaimedDomains err = nil, want a refusal")
			}
			for _, want := range []string{"acme.com", tc.claims[0].GetOwner()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
		})
	}

	t.Run("a hostname whose owner could not be read warns and deploys", func(t *testing.T) {
		t.Parallel()

		var warned []string
		err := refuseClaimedDomains([]*contractv1.DomainClaim{
			{Hostname: "acme.com", Cause: "the edge was throttled listing what it serves"},
		}, project.DefaultFileName, func(message string) { warned = append(warned, message) })
		if err != nil {
			t.Fatalf("refuseClaimedDomains err = %v, want a deploy that continues when the provider could not say who serves the hostname", err)
		}
		if len(warned) != 1 || !strings.Contains(warned[0], "acme.com") || !strings.Contains(warned[0], "throttled") {
			t.Errorf("warnings = %q, want one naming the hostname and why its owner could not be read", warned)
		}
	})

	t.Run("every claimed hostname is named, and no unclaimed one", func(t *testing.T) {
		t.Parallel()

		err := refuseClaimedDomains([]*contractv1.DomainClaim{
			{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-production-web"},
			{Hostname: "www.acme.com", Status: contractv1.DomainClaim_STATUS_UNCLAIMED},
			{Hostname: "shop.acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-third-production-web"},
		}, project.DefaultFileName, func(string) {})
		if err == nil {
			t.Fatal("refuseClaimedDomains err = nil, want a refusal")
		}
		for _, want := range []string{"acme.com", "ocel-other-production-web", "shop.acme.com", "ocel-third-production-web"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "www.acme.com") {
			t.Errorf("err = %v, want the unclaimed hostname left out", err)
		}
	})
}

func relayEdge(fixture clitest.FakeProject) *fake.Edge {
	return fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay)
}

func TestAPreviewRefusesADomainAnotherProjectClaims(t *testing.T) {
	t.Run("a preview refuses a domain another project claims", func(t *testing.T) {
		fixture := setUpPreviewProject(t)
		relayEdge(fixture).Owns("*.preview.acme.com", "ocel-other-preview")
		dependencies := newTestDependencies()
		stubGit(&dependencies, "feature/login", "")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want a domain-claim refusal")
		}

		out := stdout.String()
		for _, want := range []string{"*.preview.acme.com", "ocel-other-preview"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to name %q", out, want)
			}
		}
		if strings.Contains(out, "[build]") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("a deploy refuses a domain another project claims", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		relayEdge(fixture).Owns(productionDomain, "ocel-other-production-web")

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a domain-claim refusal")
		}

		out := stdout.String()
		for _, want := range []string{productionDomain, "ocel-other-production-web"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to name %q", out, want)
			}
		}
		if strings.Contains(out, "[build]") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("a deploy declares the project's and the apps' hostnames", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node", domains: { production: "api.acme.com" } }],
};
`)
		writeAppSource(t, fixture.Root, "api")
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		preflights := sentPreflights(t, fixture)
		if len(preflights) != 1 {
			t.Fatalf("the CLI sent %d preflights, want 1", len(preflights))
		}
		if got := preflights[0]; got.GetSlug() != "test-app" || !slices.Equal(got.GetDomains(), []string{"acme.com", "api.acme.com"}) {
			t.Errorf("the preflight named slug %q and domains %v, want test-app with the declared hostnames", got.GetSlug(), got.GetDomains())
		}
	})
}

func deployOutput(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts deployOptions, stdin string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader(stdin)); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func onlyPreflight(t *testing.T, fixture clitest.FakeProject) *contractv1.PreflightRequest {
	t.Helper()
	preflights := sentPreflights(t, fixture)
	if len(preflights) != 1 {
		t.Fatalf("the CLI sent %d preflights, want 1", len(preflights))
	}
	return preflights[0]
}

func TestADeployAsksAboutTheProjectsSlugOnlyWhenItCanActOnTheAnswer(t *testing.T) {
	t.Run("declared domains pass the slug to the preflight", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)

		deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")
		if slug := onlyPreflight(t, fixture).GetSlug(); slug != "test-app" {
			t.Errorf("the preflight named slug %q, want the project's", slug)
		}
	})

	t.Run("a non-TTY stdin with no domains leaves the slug out", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)
		recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		_ = runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader(""))
		if slug := onlyPreflight(t, fixture).GetSlug(); slug != "" {
			t.Errorf("the preflight named slug %q, want a non-TTY deploy to ask for no slug-scoped answers", slug)
		}
		if strings.Contains(stdout.String(), "NEW project") {
			t.Errorf("stdout = %q, want no drift warning from a preflight that asked about no slug", stdout.String())
		}
	})

	t.Run("--yes asks the provider exactly what the same run without it would", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		terminalStdin(&dependencies)
		fixture := setUpDeployProject(t)
		recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

		out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")
		if slug := onlyPreflight(t, fixture).GetSlug(); slug != "test-app" {
			t.Errorf("the preflight named slug %q, want --yes to leave the slug-scoped question on the wire untouched", slug)
		}
		if !strings.Contains(out, "This will create a NEW project.") {
			t.Errorf("stdout = %q, want the drift warning still told to whoever passed --yes", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to grant the guard rather than raise it", out)
		}
	})
}

func TestAnInteractiveDeployWarnsAboutTheOtherProjectsOnTheBackend(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	terminalStdin(&dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	out := deployOutput(t, fixture, dependencies, deployOptions{}, "y\n")
	if slug := onlyPreflight(t, fixture).GetSlug(); slug != "test-app" {
		t.Errorf("the preflight named slug %q, want the prompting deploy to have asked with the slug", slug)
	}
	for _, want := range []string{
		"No existing deployment for slug \"test-app\".",
		"This will create a NEW project.",
		"This backend already has: billing, my-application",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestDeployYesBypassesTheSlugDriftGuard(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")
	if strings.Contains(out, "[y/N]") {
		t.Errorf("stdout = %q, want --yes to bypass the drift prompt", out)
	}
	if !strings.Contains(out, "Deployed") {
		t.Errorf("stdout = %q, want the deploy to proceed", out)
	}
}

func TestTheIdentityBannerPrintsBeforeTheBuildAndTheDeploy(t *testing.T) {
	t.Run("on a terminal", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		pretendStdoutIsTerminal(&dependencies)
		fixture := setUpDeployProject(t)
		fixture.Provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{Account: "abcd1234"}, nil)

		out := ansi.Strip(deployOutput(t, fixture, dependencies, deployOptions{yes: true}, ""))
		for _, want := range []string{"ocel", "test-app › production", "fake", "000000000000", "fake/reference", "edge", "abcd1234"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
		banner := strings.Index(out, "test-app › production")
		built := strings.Index(out, "[build]")
		deployed := strings.Index(out, "[provision]")
		if banner < 0 || built < 0 || deployed < 0 {
			t.Fatalf("expected banner, build, and deploy all present; banner=%d build=%d deploy=%d\n%s", banner, built, deployed, out)
		}
		if banner >= built || built >= deployed {
			t.Errorf("expected order banner < build < deploy; got banner=%d build=%d deploy=%d\n%s", banner, built, deployed, out)
		}
	})

	t.Run("with no terminal to print it to", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)
		fixture.Provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{Account: "abcd1234"}, nil)

		out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")
		for _, want := range []string{"ocel  dev  test-app › production", "  fake  000000000000 · as fake/reference\n  edge  abcd1234"} {
			if !strings.Contains(out, want+"\n") {
				t.Errorf("stdout missing %q with no terminal attached:\n%s", want, out)
			}
		}
		if strings.Contains(out, "\x1b[") {
			t.Errorf("the banner painted colour with no terminal attached:\n%q", out)
		}
	})
}

func TestACredentialProblemAbortsTheDeployBeforeTheBuild(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	pretendStdoutIsTerminal(&dependencies)
	fixture := setUpDeployProject(t)
	fixture.Provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{}, errors.New("the relay token was revoked"))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want a credential-check error")
	}

	out := stdout.String()
	if !strings.Contains(out, "000000000000") {
		t.Errorf("stdout = %q, want the resolved identity still shown", out)
	}
	if !strings.Contains(out, "relay token was revoked") {
		t.Errorf("stdout = %q, want the relay credential problem surfaced", out)
	}
	if strings.Contains(out, "[build]") {
		t.Errorf("stdout = %q, want the build to be skipped on a credential failure", out)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys, want none", len(sent))
	}
}

func TestADeployRefusesWithoutProductionInfrastructure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, fixture clitest.FakeProject)
		want    string
	}{
		{
			name: "a tier mismatch refuses without deploying",
			arrange: func(t *testing.T, fixture clitest.FakeProject) {
				removeBootstrap(t, fixture, environment.TierProduction)
				bootstrapTier(t, fixture, environment.TierPreview)
			},
			want: "this command needs production infrastructure",
		},
		{
			name: "absent infrastructure refuses without deploying",
			arrange: func(t *testing.T, fixture clitest.FakeProject) {
				removeBootstrap(t, fixture, environment.TierProduction)
			},
			want: "ocel bootstrap",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			fixture := setUpDeployProject(t)
			tc.arrange(t, fixture)

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("runDeploy err = nil, want a refusal")
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.want)
			}
			if sent := sentDeploys(t, fixture); len(sent) != 0 {
				t.Errorf("the CLI sent %d deploys, want none", len(sent))
			}
		})
	}
}

func TestRunDeployRefusesAComputeTheProviderDoesNotRun(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeServerless} })
	writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api", compute: "container" }`)
	writeAppSource(t, fixture.Root, "api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{`"api"`, `"container"`, "fake", "serverless"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want the refusal to name %s", out, want)
		}
	}
}

func TestDeployChecksCredentialsAndTheProjectsBootstrapAsACheckSpanNamedForItsProviderThenSaysWhoItActsAs(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	fixture := setUpDeployProject(t)

	out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")

	evs := envelopes(t, out)
	started := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetStarted() != nil && ev.GetOperation().GetMessage() == "Checking your credentials and the production bootstrap for "+clitest.FixtureSlug
	})
	if started < 0 {
		t.Fatalf("no span started for the credential check: %s", out)
	}
	span := evs[started]
	if span.GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK || span.GetOperation().GetSubject() != "fake" {
		t.Errorf("credential check started in %s naming %q, want the check phase naming the provider %q", span.GetOperation().GetPhase(), span.GetOperation().GetSubject(), "fake")
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetEnded() != nil && bytes.Equal(ev.GetOperation().GetSpanId(), span.GetOperation().GetSpanId())
	})
	if ended < 0 || evs[ended].GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Fatalf("the credential check never ended OK: %s", out)
	}
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < started || identity > ended {
		t.Errorf("identity at event %d, credential check from %d to %d: want who the deploy acts as named while the check is still open", identity, started, ended)
	}
	if evs[identity].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("identity in %s, want the check phase", evs[identity].GetOperation().GetPhase())
	}
}

func TestAnUnbootstrappedProductionFailsTheCheckSpanWithTheCommandThatBootstrapsIt(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatalf("runDeploy succeeded against no bootstrap: %s", stdout.String())
	}

	evs := envelopes(t, stdout.String())
	check := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetStarted() != nil && strings.HasPrefix(ev.GetOperation().GetMessage(), "Checking your credentials")
	})
	if check < 0 {
		t.Fatalf("no credential check started: %s", stdout.String())
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetEnded() != nil && bytes.Equal(ev.GetOperation().GetSpanId(), evs[check].GetOperation().GetSpanId())
	})
	if ended < 0 || evs[ended].GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR || !strings.Contains(evs[ended].GetOperation().GetMessage(), "ocel bootstrap production") {
		t.Fatalf("the credential check did not fail naming `ocel bootstrap production`: %s", stdout.String())
	}
}

func TestDeploysEventsAreInTheCheckPhaseThenBuildThenTheProvidersDeployPhases(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	fixture := setUpDeployProject(t)

	out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")

	var order []progressv1.Phase
	for _, ev := range envelopes(t, out) {
		phase := ev.GetOperation().GetPhase()
		if phase == progressv1.Phase_PHASE_UNSPECIFIED || (len(order) > 0 && order[len(order)-1] == phase) {
			continue
		}
		order = append(order, phase)
	}
	deploying := []progressv1.Phase{progressv1.Phase_PHASE_PROVISION, progressv1.Phase_PHASE_DEPLOY, progressv1.Phase_PHASE_PROMOTE}
	if len(order) < 3 || order[0] != progressv1.Phase_PHASE_CHECK || order[1] != progressv1.Phase_PHASE_BUILD {
		t.Fatalf("phases in order %v, want check, then build, then the provider's deploy phases", order)
	}
	for _, phase := range order[2:] {
		if !slices.Contains(deploying, phase) {
			t.Errorf("phases in order %v: %s after the build, want only the provider's deploy phases %v", order, phase, deploying)
		}
	}
}

func setUpProjectLackingFeatures(t *testing.T) clitest.FakeProject {
	t.Helper()
	fixture := clitest.SetUpProject(t)
	clitest.Bootstrap(t, fixture.Provider, environment.TierProduction)
	writeUsageMonorepo(t, fixture.Root, "")
	return fixture
}

const bootstrapCommand = "Run `ocel bootstrap production --features cache,images` and try again"

func TestDeployYesNeverStopsToAsk(t *testing.T) {
	fixture := setUpProjectLackingFeatures(t)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	terminalStdin(&dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("an unattended deploy against a bootstrap missing a feature it needs was allowed through")
	}
	if !strings.Contains(stdout.String(), bootstrapCommand) {
		t.Errorf("stdout = %q, want the literal command to run", stdout.String())
	}
	if bootstraps := clitest.RequestsTo[*contractv1.BootstrapRequest](t, fixture.Requests, contractv1connect.ProviderServiceBootstrapProcedure); len(bootstraps) != 0 || len(sentDeploys(t, fixture)) != 0 {
		t.Errorf("the provider was asked to bootstrap or deploy; --yes answers questions about the deploy, it does not order a bootstrap")
	}
}

func TestDeployWithoutATerminalRefusesTheBootstrapItCannotOffer(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts deployOptions
	}{
		{name: "without --yes", opts: deployOptions{}},
		{name: "with --yes", opts: deployOptions{yes: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpProjectLackingFeatures(t)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, apiFunction())

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := runDeploy(context.Background(), dependencies, fixture.Root, tc.opts, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
			}
			if !strings.Contains(stdout.String(), bootstrapCommand) {
				t.Errorf("stdout = %q, want the literal command to run", stdout.String())
			}
			if bootstraps := clitest.RequestsTo[*contractv1.BootstrapRequest](t, fixture.Requests, contractv1connect.ProviderServiceBootstrapProcedure); len(bootstraps) != 0 || len(sentDeploys(t, fixture)) != 0 {
				t.Error("the provider was asked to bootstrap or deploy; a bootstrap nobody can be offered is never ordered, --yes or not")
			}
		})
	}
}

func writeAppNeeds(t *testing.T, root, app, framework, needs string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", app, edge.ServeDescriptorFile),
		`{"framework":"`+framework+`","buildId":"b1","needs":`+needs+`}`)
}

const middlewareNeeds = `{"edge-middleware":{"count":2,"routes":["/dashboard","/admin"]}}`

func setUpNeedsProject(t *testing.T, fields, needs string) (clitest.FakeProject, Dependencies) {
	t.Helper()
	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, fields)
	writeAppNeeds(t, fixture.Root, "api", "node", needs)
	relayEdge(fixture).Serves([]edge.Need{})
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	return fixture, dependencies
}

func TestDeployRendersTheNeedsRefusalInHumanMode(t *testing.T) {
	fixture, dependencies := setUpNeedsProject(t, "", middlewareNeeds)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the unsupported need to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	for _, want := range []string{"edge-middleware", "next start", "/dashboard", "/admin", `"edge-middleware" to ` + "`allowDegraded`"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to include %q", rendered, want)
		}
	}
}

func TestDeployRendersTheNeedsRefusalInJSONMode(t *testing.T) {
	fixture, dependencies := setUpNeedsProject(t, "", middlewareNeeds)
	useJSONLogFormat(t, &dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the unsupported need to fail the deploy; stdout=%s", stdout.String())
	}

	var message string
	for _, ev := range envelopes(t, stdout.String()) {
		if res := ev.GetSummary(); res != nil {
			if res.GetSuccess() {
				t.Fatalf("run result reports success, want the unsupported need to fail the run: %s", stdout.String())
			}
			message = res.GetDetail()
		}
	}
	if message == "" {
		t.Fatalf("no failing run result on the stream: %s", stdout.String())
	}
	for _, want := range []string{"edge-middleware", "next start", "/dashboard", `"edge-middleware" to ` + "`allowDegraded`"} {
		if !strings.Contains(message, want) {
			t.Errorf("failed record error = %q, want it to include %q", message, want)
		}
	}
}

func TestDeployRendersADegradedNeedInHumanMode(t *testing.T) {
	fixture, dependencies := setUpNeedsProject(t, "  allowDegraded: [\"edge-middleware\"],\n", middlewareNeeds)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	for _, want := range []string{"edge-middleware", "next start", "/dashboard"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want the need named and the degrade spelled out (%q)", rendered, want)
		}
	}
}

func TestDeployRendersADegradedNeedAsACheckPhaseWarningInJSON(t *testing.T) {
	fixture, dependencies := setUpNeedsProject(t, "  allowDegraded: [\"edge-middleware\", \"ppr-resume\"],\n",
		`{"edge-middleware":{"count":1,"routes":["/dashboard"]},"ppr-resume":{"count":1,"routes":["/"]}}`)
	useJSONLogFormat(t, &dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var degraded []string
	for _, ev := range envelopes(t, stdout.String()) {
		if ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetOperation().GetPhase() == progressv1.Phase_PHASE_CHECK {
			degraded = append(degraded, ev.GetOperation().GetMessage())
		}
	}
	if len(degraded) != 2 {
		t.Fatalf("got %d check-phase warnings, want one per waived need: %s", len(degraded), stdout.String())
	}
	if !strings.HasPrefix(degraded[0], "edge-middleware ") || !strings.HasPrefix(degraded[1], "ppr-resume ") {
		t.Errorf("check-phase warnings = %q, want edge-middleware then ppr-resume", degraded)
	}
	if !strings.Contains(degraded[0], "next start") {
		t.Errorf("degraded warning = %q, want the degrade spelled out", degraded[0])
	}
}

func TestDeploySaysNothingAboutNeedsForAnAppThatDeclaresNone(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		_, out, err := deployUsageMonorepo(t, "")
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		for _, unwanted := range []string{"next start", "allowDegraded", "degraded"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("rendered output = %q, want no needs notice for an app that declares none (%q)", out, unwanted)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		writeUsageMonorepo(t, fixture.Root, "")
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		useJSONLogFormat(t, &dependencies)

		out := deployOutput(t, fixture, dependencies, deployOptions{yes: true}, "")
		for _, ev := range envelopes(t, out) {
			if ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetOperation().GetPhase() == progressv1.Phase_PHASE_CHECK {
				t.Errorf("check-phase warning %q on the stream, want none for an app that declares no needs", ev.GetOperation().GetMessage())
			}
		}
	})
}

func lintEdgeWarnings(t *testing.T, cfg *project.Project) []string {
	t.Helper()
	definition := &resourcesv1.VariableDefinition{
		Key:    "STRIPE_KEY",
		Class:  resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		Source: "env.ts",
	}
	onEdge, err := build.EdgeApps(cfg.Dir)
	if err != nil {
		t.Fatalf("EdgeApps: %v", err)
	}
	warnings, err := variables.LintEdgeSecrets(
		[]*resourcesv1.VariableDefinition{definition},
		variablescope.Apps(cfg),
		onEdge,
	)
	if err != nil {
		t.Fatalf("LintEdge: %v", err)
	}
	return warnings
}

func TestEdgeAppsReadsTheNeeds(t *testing.T) {
	t.Parallel()

	t.Run("a need for edge code names the app and warns about the secret", func(t *testing.T) {
		t.Parallel()

		cfg := &project.Project{Dir: t.TempDir(), Apps: []project.App{{Name: "web", Path: "."}}}
		writeAppNeeds(t, cfg.Dir, "web", "next", `{"edge-runtime":{"count":1,"routes":["/edgy"]}}`)

		if apps, err := build.EdgeApps(cfg.Dir); err != nil || len(apps) != 1 || apps[0] != "web" {
			t.Fatalf("EdgeApps = %v, want the project's sole app", apps)
		}
		if warnings := lintEdgeWarnings(t, cfg); len(warnings) != 1 {
			t.Fatalf("warnings = %q, want the secret warning", warnings)
		}
	})

	t.Run("a node app needs nothing and lints clean", func(t *testing.T) {
		t.Parallel()

		cfg := &project.Project{Dir: t.TempDir(), Apps: []project.App{{Name: "api", Path: "."}}}
		writeAppNeeds(t, cfg.Dir, "api", "express", `{}`)

		if apps, err := build.EdgeApps(cfg.Dir); err != nil || len(apps) != 0 {
			t.Fatalf("EdgeApps = %v, want none", apps)
		}
		if warnings := lintEdgeWarnings(t, cfg); len(warnings) != 0 {
			t.Fatalf("warnings = %q, want none", warnings)
		}
	})

	t.Run("needs that ship no customer code name no edge app", func(t *testing.T) {
		t.Parallel()

		cfg := &project.Project{Dir: t.TempDir()}
		writeAppNeeds(t, cfg.Dir, "web", "next",
			`{"edge-cache":{"count":3},"streaming":{"count":2},"ppr-resume":{"count":1,"routes":["/"]}}`)

		if apps, err := build.EdgeApps(cfg.Dir); err != nil || len(apps) != 0 {
			t.Fatalf("EdgeApps = %v, want none", apps)
		}
	})
}

func pretendStdoutIsTerminal(dependencies *Dependencies) {
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{TTY: true})
	}
}
