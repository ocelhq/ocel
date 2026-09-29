package deploy

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
	"google.golang.org/protobuf/encoding/protojson"
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

func TestAPreviewRefusesADomainAnotherProjectClaims(t *testing.T) {
	t.Run("a preview refuses a domain another project claims", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
		dependencies := newTestDependencies()
		stubGit(&dependencies, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainOwnerEnvVar, "ocel-other-preview")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
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
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a deploy refuses a domain another project claims", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "acme.com" },
};
`)
		t.Setenv(clitest.FakeDomainOwnerEnvVar, "ocel-other-production-web")

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a domain-claim refusal")
		}

		out := stdout.String()
		for _, want := range []string{"acme.com", "ocel-other-production-web"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to name %q", out, want)
			}
		}
		if strings.Contains(out, "[build]") {
			t.Errorf("stdout = %q, want the refusal before anything is built", out)
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a deploy declares the project's and the apps' hostnames", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node", domains: { production: "api.acme.com" } }],
};
`)
		writeAppSource(t, root, "api")
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app domains=acme.com,api.acme.com") {
			t.Errorf("stdout = %q, want the declared hostnames to have reached Preflight", stdout.String())
		}
	})
}

func TestDeployChecksCredentialsAndTheProjectsBootstrapAsACheckUnitNamedForItsProviderThenSaysWhoItActsAs(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := envelopes(t, stdout.String())
	started := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetStarted() != nil && ev.GetMessage() == "Checking your credentials and the production bootstrap for "+clitest.FixtureSlug
	})
	if started < 0 {
		t.Fatalf("no unit started for the credential check: %s", stdout.String())
	}
	unit := evs[started]
	if unit.GetPhase() != progressv1.Phase_PHASE_CHECK || unit.GetSubject() != "fake" {
		t.Errorf("credential check started in %s naming %q, want the check phase naming the provider %q", unit.GetPhase(), unit.GetSubject(), "fake")
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), unit.GetSpanId())
	})
	if ended < 0 || evs[ended].GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Fatalf("the credential check never ended OK: %s", stdout.String())
	}
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < started || identity > ended {
		t.Errorf("identity at event %d, credential check from %d to %d: want who the deploy acts as named while the check is still open", identity, started, ended)
	}
	if evs[identity].GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("identity in %s, want the check phase", evs[identity].GetPhase())
	}
}

func TestAnUnbootstrappedProductionFailsTheCheckUnitWithTheCommandThatBootstrapsIt(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatalf("runDeploy succeeded against no bootstrap: %s", stdout.String())
	}

	evs := envelopes(t, stdout.String())
	check := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetStarted() != nil && strings.HasPrefix(ev.GetMessage(), "Checking your credentials")
	})
	if check < 0 {
		t.Fatalf("no credential check started: %s", stdout.String())
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), evs[check].GetSpanId())
	})
	if ended < 0 || evs[ended].GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR || !strings.Contains(evs[ended].GetMessage(), "ocel bootstrap production") {
		t.Fatalf("the credential check did not fail naming `ocel bootstrap production`: %s", stdout.String())
	}
}

func TestDeploysEventsAreInTheCheckPhaseThenBuildThenTheProvidersDeployPhases(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var order []progressv1.Phase
	for _, ev := range envelopes(t, stdout.String()) {
		phase := ev.GetPhase()
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

func TestDeployYesNeverStopsToAsk(t *testing.T) {
	root, journal := clitest.SetUpEdgeFixture(t, "")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("an unattended deploy against a bootstrap missing a feature it needs was allowed through")
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features image-optimization,isr` and try again") {
		t.Errorf("stdout = %q, want the literal command to run", stdout.String())
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Errorf("the provider was reached; --yes answers questions about the deploy, it does not order a bootstrap")
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
			root, journal := clitest.SetUpEdgeFixture(t, "")
			dependencies := newTestDependencies()
			stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
			t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := runDeploy(context.Background(), dependencies, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
			}
			if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features image-optimization,isr` and try again") {
				t.Errorf("stdout = %q, want the literal command to run", stdout.String())
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Error("the provider was reached; a bootstrap nobody can be offered is never ordered, --yes or not")
			}
		})
	}
}

const needsRefusal = "app web needs edge-middleware and the \"direct\" edge does not serve it: middleware runs in the origin's Node server the way `next start` runs it, so every request pays the round trip to the origin before it is routed. " +
	"It affects routes /dashboard, /admin. " +
	"Add \"edge-middleware\" to `allowDegraded` in ocel.config.ts to deploy it degraded, or move the app to an edge that serves edge-middleware"

const degradedDetail = "web: middleware runs in the origin's Node server the way `next start` runs it, so every request pays the round trip to the origin before it is routed. It affects routes /dashboard"

func useJSONLogFormat(t *testing.T, dependencies *Dependencies) {
	t.Helper()
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}
}

func envelopes(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var events []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestDeployRendersTheNeedsRefusalInHumanMode(t *testing.T) {
	root, _ := clitest.SetUpEdgeFixture(t, "")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	t.Setenv(clitest.FakeNeedsRefusalEnvVar, needsRefusal)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	root, _ := clitest.SetUpEdgeFixture(t, "")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	useJSONLogFormat(t, &dependencies)
	t.Setenv(clitest.FakeNeedsRefusalEnvVar, needsRefusal)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	root, _ := clitest.SetUpEdgeFixture(t, "  allowDegraded: [\"edge-middleware\"],\n")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	t.Setenv(clitest.FakeDegradedEnvVar, "edge-middleware="+degradedDetail)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	root, _ := clitest.SetUpEdgeFixture(t, "  allowDegraded: [\"edge-middleware\", \"ppr-resume\"],\n")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	useJSONLogFormat(t, &dependencies)
	t.Setenv(clitest.FakeDegradedEnvVar, "edge-middleware="+degradedDetail+";ppr-resume=web: the shell comes from the origin. It affects routes /")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var degraded []string
	for _, ev := range envelopes(t, stdout.String()) {
		if ev.GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetPhase() == progressv1.Phase_PHASE_CHECK {
			degraded = append(degraded, ev.GetMessage())
		}
	}
	if len(degraded) != 2 {
		t.Fatalf("got %d check-phase warnings, want one per waived need: %s", len(degraded), stdout.String())
	}
	if !strings.HasPrefix(degraded[0], "edge-middleware: ") || !strings.HasPrefix(degraded[1], "ppr-resume: ") {
		t.Errorf("check-phase warnings = %q, want edge-middleware then ppr-resume", degraded)
	}
	if !strings.Contains(degraded[0], "next start") {
		t.Errorf("degraded warning = %q, want the degrade spelled out", degraded[0])
	}
}

func TestDeploySaysNothingAboutNeedsForAnAppThatDeclaresNone(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		root, _ := clitest.SetUpEdgeFixture(t, "")
		dependencies := newTestDependencies()
		stubBuild(&dependencies, clitest.UsageMonorepoFunctions())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		rendered := stdout.String() + stderr.String()
		for _, unwanted := range []string{"next start", "allowDegraded", "degraded"} {
			if strings.Contains(rendered, unwanted) {
				t.Errorf("rendered output = %q, want no needs notice for an app that declares none (%q)", rendered, unwanted)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		root, _ := clitest.SetUpEdgeFixture(t, "")
		dependencies := newTestDependencies()
		stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
		useJSONLogFormat(t, &dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		for _, ev := range envelopes(t, stdout.String()) {
			if ev.GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetPhase() == progressv1.Phase_PHASE_CHECK {
				t.Errorf("check-phase warning %q on the stream, want none for an app that declares no needs", ev.GetMessage())
			}
		}
	})
}

func writeAppNeeds(t *testing.T, root, app, framework, needs string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", app, edge.ServeDescriptorFile),
		`{"framework":"`+framework+`","buildId":"b1","needs":`+needs+`}`)
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
