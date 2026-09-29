package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var nodeLine = regexp.MustCompile(`(?m)^(  ✓ node is needed — .*) — node .* on PATH$`)

func rendered(t *testing.T, out string) string {
	t.Helper()
	return nodeLine.ReplaceAllString(out, "$1 — node vX on PATH")
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	code, ok := exitcode.Of(err)
	if !ok {
		t.Fatalf("err = %v, want an exit signal", err)
	}
	return code
}

func TestDoctorRendersEveryVerdict(t *testing.T) {
	t.Parallel()

	var found report
	project := section{name: "Project", identity: "my-shop · ocel.config.ts"}
	project.pass("config loads — 2 apps (web, api)")
	found.add(project)

	edge := section{name: "Relay"}
	edge.fail("FAKE_RELAY_TOKEN rejected", "create a token with the scopes from `ocel permissions deploy`")
	found.add(edge)

	preview := section{name: "Preview"}
	preview.warn("no preview domain", "run `ocel domain use '*.preview.example.com' --preview`")
	preview.neutral("not set up — run `ocel bootstrap preview` to add previews")
	found.add(preview)

	var out bytes.Buffer
	found.render(&out, newPaint(&out))

	want := strings.Join([]string{
		"Project  my-shop · ocel.config.ts",
		"  ✓ config loads — 2 apps (web, api)",
		"",
		"Relay",
		"  ✗ FAKE_RELAY_TOKEN rejected",
		"    → create a token with the scopes from `ocel permissions deploy`",
		"",
		"Preview",
		"  ⚠ no preview domain",
		"    → run `ocel domain use '*.preview.example.com' --preview`",
		"  – not set up — run `ocel bootstrap preview` to add previews",
		"",
		"1 problem, 1 warning.",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestDoctorSummaryCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		failures int
		warnings int
		want     string
	}{
		{"nothing to report", 0, 0, "Good to go."},
		{"one of each", 1, 1, "1 problem, 1 warning."},
		{"several problems", 2, 1, "2 problems, 1 warning."},
		{"warnings alone", 0, 3, "3 warnings."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var found report
			s := section{name: "Project"}
			for range tt.failures {
				s.fail("broken", "")
			}
			for range tt.warnings {
				s.warn("shaky", "")
			}
			found.add(s)

			var out bytes.Buffer
			if got := found.summary(newPaint(&out)); got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDoctorNamesWhatIsStale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"ocel-bootstrap-isr"}, "ocel-bootstrap-isr is stale"},
		{[]string{"a", "b"}, "a, b are stale"},
	}
	for _, tt := range tests {
		if got := listText(tt.names, "stale"); got != tt.want {
			t.Errorf("listText(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestRunDoctorWithoutAConfig(t *testing.T) {
	root := t.TempDir()
	deps := clitest.NewDeps()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, root, &stdout)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ✗ no ocel.json, ocel.yaml, ocel.yml or ocel.config.ts found in this directory or any parent",
		"    → run `ocel init` to set up this project",
		"1 problem.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Production") || strings.Contains(out, "Preview") {
		t.Errorf("a project without a config named the tiers anyway; got:\n%s", out)
	}
}

func healthyProject(t *testing.T) clitest.FakeProject {
	t.Helper()

	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { fake: {} },
  domains: { production: "shop.example.com", preview: "*.preview.example.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	clitest.WriteFile(t, filepath.Join(project.Root, "apps", "web", "src", "server.ts"), "export function handler() {}\n")
	clitest.WriteFile(t, filepath.Join(project.Root, "apps", "api", "src", "server.ts"), "export function handler() {}\n")
	return project
}

func record(t *testing.T, p *fake.Provider, key keyvalue.Key, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.KeyValues().Write(context.Background(), keyvalue.Entry{Key: key, Value: encoded}); err != nil {
		t.Fatalf("record %s: %v", key, err)
	}
}

func recordPreviewWildcard(t *testing.T, p *fake.Provider, baseDomain string) {
	t.Helper()
	record(t, p, stackrecords.WildcardKey(environment.TierPreview), stackrecords.Wildcard{BaseDomain: baseDomain, Edge: fake.KindRelay})
}

func TestRunDoctorOnAHealthyProject(t *testing.T) {
	project := healthyProject(t)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	want := strings.Join([]string{
		"Project  my-shop · ocel.config.ts",
		"  ✓ node is needed — ocel.config.ts is TypeScript, this project contains JavaScript — node vX on PATH",
		"  ✓ config loads — 2 apps (web, api)",
		"  ✓ provider fake " + version.Version + "",
		"  ✓ provider default edge",
		"",
		"Fake  000000000000 · fake/reference",
		"  ✓ credentials valid",
		"",
		"Production  shop.example.com",
		"  ✓ bootstrapped — schema 1, current",
		"",
		"Preview  *.preview.example.com",
		"  ✓ bootstrapped — schema 1, current",
		"",
		"Good to go.",
		"",
	}, "\n")
	if got := rendered(t, stdout.String()); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

func TestDoctorAsksTheProviderAboutTheContainerAppsADeployWould(t *testing.T) {
	project := healthyProject(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { fake: {} },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", compute: "container" },
  ],
};
`)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	sent := clitest.RequestsTo[*contractv1.PreflightRequest](t, project.Requests, contractv1connect.ProviderServicePreflightProcedure)
	if len(sent) == 0 {
		t.Fatal("doctor sent no preflight")
	}
	for _, req := range sent {
		if len(req.GetContainers()) != 1 || req.GetContainers()[0].GetApp() != "api" {
			t.Errorf("doctor sent preflight naming containers %v, want the container app api as a deploy's preflight does", req.GetContainers())
		}
	}
}

func TestRunDoctorReportsACredentialProblem(t *testing.T) {
	project := healthyProject(t)
	project.Provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{}, refusal.Refuse(refusal.CodeDenied, "configure the credential and re-run"))

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"Fake  000000000000 · fake/reference\n  ✓ credentials valid",
		"Relay\n  ✗ could not authenticate\n    → configure the credential and re-run",
		"1 problem.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorWarnsAboutAStaleStackNoFeatureRequires(t *testing.T) {
	project := healthyProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierProduction, fake.FeatureCache)
	project.Provider.FakeBootstrap().MarkStale(fake.FeatureCache)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want warnings alone to pass; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ⚠ fake-production-cache is stale",
		"    → run `ocel bootstrap production` to refresh it",
		"1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorFailsAnUnfinishedBootstrap(t *testing.T) {
	project := healthyProject(t)
	project.Provider.FakeBootstrap().MarkUnfinished()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want an unfinished apply to fail; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ✗ an apply never finished, so nothing recorded is a claim about what is provisioned",
		"    → run `ocel bootstrap production` to plan the work that is left and finish it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorWarnsAboutAStaleBootstrap(t *testing.T) {
	project := healthyProject(t)
	project.Provider.FakeBootstrap().MarkStale("")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want warnings alone to pass; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ⚠ fake-production is stale",
		"    → run `ocel bootstrap production` to refresh it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoctorReadsTheBootstrapAndNothingThatGrowsWithTheAccount(t *testing.T) {
	project := healthyProject(t)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stderr=%s", err, stderr.String())
	}
	asked := clitest.RequestsTo[*contractv1.DescribeBootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceDescribeBootstrapProcedure)
	if len(asked) != 2 {
		t.Fatalf("the provider was asked %d times, want once per tier: %v", len(asked), asked)
	}
	for _, req := range asked {
		if req.GetWithDependents() {
			t.Errorf("doctor asked %v; it renders no dependent, and reading them costs one query per project in the account", asked)
		}
	}
}

func TestRunDoctorServesPreviewsOnTheGlobalWildcardWithoutAWarning(t *testing.T) {
	project := healthyProject(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { fake: {} },
  domains: { production: "shop.example.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	recordPreviewWildcard(t, project.Provider, "preview.ocel.app")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	if strings.Contains(out, "no preview domain") {
		t.Errorf("doctor warned about a preview domain the global wildcard already supplies:\n%s", out)
	}
	for _, want := range []string{"Preview  *.preview.ocel.app (global)", "Good to go."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorNotesAProjectPreviewDomainShadowingTheGlobalOne(t *testing.T) {
	project := healthyProject(t)
	recordPreviewWildcard(t, project.Provider, "preview.ocel.app")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{"Preview  *.preview.example.com", "  – project-level preview domain; global *.preview.ocel.app ignored", "Good to go."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorLeavesAnUnwantedTierAlone(t *testing.T) {
	project := clitest.SetUpProject(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { fake: {} },
  domains: { production: "shop.example.com" },
};
`)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want a tier nobody asked for to pass; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"Preview\n  ⚠ no preview domain\n    → run `ocel domain use '*.preview.example.com' --preview`\n  – not set up — run `ocel bootstrap preview` to add previews\n",
		"1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorPrintsTheHostCheckFindingsAndTheCertificatesAndRefusesNothing(t *testing.T) {
	project := healthyProject(t)
	p := project.Provider
	p.WithHooks(func(hooks *provider.Hooks) {
		hooks.CheckHost = func(_ context.Context, req provider.HostCheckRequest) ([]provider.HostCheck, error) {
			checks := []provider.HostCheck{{Subject: "port 80", Finding: "something listens on port 80"}}
			for _, hostname := range req.Hostnames {
				checks = append(checks, provider.HostCheck{
					Subject: hostname,
					Verdict: provider.HostNeedsAction,
					Finding: hostname + " does not resolve",
					Fix:     "add the record `ocel domain add` printed",
				})
			}
			return checks, nil
		}
	})
	recordPreviewWildcard(t, p, "preview.example.com")
	p.ReportCertificateFor("*.preview.example.com", provider.CertificateHealth{Renewal: "you placed it on this box and you renew it", ExpiresAt: 4102444800})
	edgeState := stackrecords.EdgeState{Kind: fake.KindRelay, Edge: edge.StackState{Slug: "my-shop", Tier: environment.TierProduction}}
	edgeState.SetHost("shop.example.com", stackrecords.HostnameState{Edge: fake.KindRelay, Certificate: provider.Certificate{ID: "proxy:shop.example.com"}})
	record(t, p, stackrecords.EdgeStackKey(environment.TierProduction, "my-shop"), edgeState)
	p.ReportCertificateFor("shop.example.com", provider.CertificateHealth{Renewal: "SUCCESS", ExpiresAt: 4102444800})

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	out := rendered(t, stdout.String())

	if !strings.Contains(out, "Host checks") || !strings.Contains(out, "Certificates") {
		t.Fatalf("doctor printed neither section, so this run is not the window an absence can be read over:\n%s", out)
	}
	for _, want := range []string{
		"  ✓ something listens on port 80",
		"  ⚠ shop.example.com does not resolve",
		"    → add the record `ocel domain add` printed",
		"  ⚠ *.preview.example.com does not resolve",
		"*.preview.example.com — expires 2100-01-01T00:00:00Z, you placed it on this box and you renew it",
		"shop.example.com — expires 2100-01-01T00:00:00Z, SUCCESS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
	if code := exitCode(t, err); code != 0 {
		t.Errorf("exit code = %d over the output above, want 0: a host check is a report and never a gate, and a manual record is the normal state", code)
	}
	if strings.Contains(out, failGlyph) {
		t.Errorf("doctor refused something on a bootstrapped box whose only finding is a manual record:\n%s", out)
	}
}

func TestRunDoctorWarnsThatNothingRenewsAPinnedWildcardAboutToExpire(t *testing.T) {
	project := healthyProject(t)
	recordPreviewWildcard(t, project.Provider, "preview.example.com")
	project.Provider.ReportCertificateFor("*.preview.example.com", provider.CertificateHealth{
		Renewal:      "you placed it on this box and you renew it",
		ExpiresAt:    time.Now().Add(72 * time.Hour).Unix(),
		ExpiringSoon: true,
	})

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	err := Run(context.Background(), deps, project.Root, &stdout)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want a warning rather than a refusal; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := rendered(t, stdout.String())
	for _, want := range []string{
		"EXPIRING SOON",
		"you placed it on this box and you renew it",
		"    → replace it before it expires; nothing here renews a certificate you pinned",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoctorChecksTheSetupInTheCheckPhaseOfItsRunAndPrintsItsReportAloneOnStdout(t *testing.T) {
	project := healthyProject(t)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, project.Root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var units []progressv1.Phase
	var result *streamv1.RunSummary
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("stream line %q is not a protojson RunEvent: %v", line, err)
		}
		if ev.GetStarted() != nil && len(ev.GetStarted().GetParentSpanId()) > 0 {
			units = append(units, ev.GetPhase())
		}
		if ev.GetSummary() != nil {
			result = ev.GetSummary()
		}
	}
	if len(units) == 0 || units[0] != progressv1.Phase_PHASE_CHECK {
		t.Errorf("unit phases = %v, want the setup checked in a unit of the check phase", units)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the doctor's run to succeed", result)
	}
	if !strings.Contains(stdout.String(), "Good to go.") || strings.Contains(stderr.String(), "Good to go.") {
		t.Errorf("stdout = %q, stream = %q: want the report on stdout and not on the stream", stdout.String(), stderr.String())
	}
}
