package doctor

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

var nodeLine = regexp.MustCompile(`(?m)^  ✓ node .* on PATH$`)

func rendered(t *testing.T, out string) string {
	t.Helper()
	return nodeLine.ReplaceAllString(out, "  ✓ node vX on PATH")
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	code, ok := exitsig.ExitCode(err)
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

	edge := section{name: "Cloudflare"}
	edge.fail("CLOUDFLARE_API_TOKEN rejected", "create a token with the scopes from `ocel permissions deploy`")
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
		"Cloudflare",
		"  ✗ CLOUDFLARE_API_TOKEN rejected",
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
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ✗ no ocel.config.ts found in this directory or any parent",
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

func healthyProject(t *testing.T) string {
	t.Helper()

	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.example.com", preview: "*.preview.example.com" },
  apps: [
    { name: "web", path: "apps/web", runtime: "node" },
    { name: "api", path: "apps/api", runtime: "node" },
  ],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), "export function handler() {}\n")
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export function handler() {}\n")
	clitest.WriteFile(t, filepath.Join(root, "node_modules", "@ocel", "provider-aws", "package.json"), `{"name":"@ocel/provider-aws","version":"1.4.0"}`)

	t.Setenv(clitest.FakeIDProviderEnvVar, "aws")
	t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
	t.Setenv(clitest.FakeIDRegionEnvVar, "eu-west-1")
	t.Setenv(clitest.FakeIDProfileEnvVar, "shop")
	return root
}

func TestRunDoctorOnAHealthyProject(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	want := strings.Join([]string{
		"Project  my-shop · ocel.config.ts",
		"  ✓ node vX on PATH",
		"  ✓ config loads — 2 apps (web, api)",
		"  ✓ provider @ocel/provider-aws 1.4.0",
		"  ✓ provider default edge",
		"",
		"AWS  123456789012 · eu-west-1 · profile shop",
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

func TestRunDoctorReportsACredentialProblem(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv(clitest.FakeCredProblemEnvVar, "cloudflare")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"AWS  123456789012 · eu-west-1 · profile shop\n  ✓ credentials valid",
		"Cloudflare\n  ✗ could not authenticate\n    → configure the credential and re-run",
		"1 problem.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorWarnsAboutAStaleBootstrap(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "stale")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want warnings alone to pass; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ⚠ ocel-bootstrap-isr is stale",
		"    → run `ocel bootstrap production` to refresh it",
		"1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorFailsAnUnfinishedBootstrap(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "unfinished")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit code = %d, want an unfinished apply to fail; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ✗ an apply never finished, so nothing recorded is a claim about what stands",
		"    → run `ocel bootstrap production` to plan the work that is left and finish it",
		"1 problem.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorWarnsAboutAStaleStackNoFeatureRequires(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "stale-optional")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("exit code = %d, want warnings alone to pass; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{
		"  ⚠ ocel-bootstrap-image-optimization is stale",
		"    → run `ocel bootstrap production` to refresh it",
		"1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoctorReadsTheBootstrapAndNothingThatGrowsWithTheAccount(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	journal := filepath.Join(t.TempDir(), "describe.journal")
	t.Setenv(clitest.FakeDescribeJournalEnvVar, journal)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("Run err = %v; stderr=%s", err, stderr.String())
	}
	got := clitest.ReadJournal(t, journal)
	if len(got) != 2 {
		t.Fatalf("the provider was asked %d times, want once per class: %v", len(got), got)
	}
	for _, line := range got {
		if strings.Contains(line, "withDependents=true") {
			t.Errorf("doctor asked %v; it renders no dependent, and reading them costs one query per project in the account", got)
		}
	}
}

func TestRunDoctorServesPreviewsOnTheGlobalWildcardWithoutAWarning(t *testing.T) {
	root := healthyProject(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.example.com" },
  apps: [
    { name: "web", path: "apps/web", runtime: "node" },
    { name: "api", path: "apps/api", runtime: "node" },
  ],
};
`)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv(clitest.FakeGlobalDomainEnvVar, "previews.ocel.dev")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	if strings.Contains(out, "no preview domain") {
		t.Errorf("doctor warned about a preview domain the global wildcard already supplies:\n%s", out)
	}
	for _, want := range []string{"Preview  *.previews.ocel.dev (global)", "Good to go."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorNotesAProjectPreviewDomainShadowingTheGlobalOne(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv(clitest.FakeGlobalDomainEnvVar, "previews.ocel.dev")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := rendered(t, stdout.String())
	for _, want := range []string{"Preview  *.preview.example.com", "  – project-level preview domain; global *.previews.ocel.dev ignored", "Good to go."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunDoctorLeavesAnUnwantedTierAlone(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "my-shop",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.example.com" },
};
`)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
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

func TestRunDoctorPrintsTheStandingFindingsAndTheCertificatesAndRefusesNothing(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv(clitest.FakeStandingEnvVar, "1")
	t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.example.com")
	t.Setenv(clitest.FakeGlobalDomainRenewalEnvVar, "you placed it on this box and you renew it")
	t.Setenv(clitest.FakeGlobalDomainExpiresEnvVar, "4102444800")
	t.Setenv(clitest.FakeDomainCertEnvVar, "SERVING proxy:shop.example.com")
	t.Setenv(clitest.FakeDomainRenewalEnvVar, "SUCCESS")
	t.Setenv(clitest.FakeDomainExpiresEnvVar, "4102444800")

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
	out := rendered(t, stdout.String())

	if !strings.Contains(out, "Standing") || !strings.Contains(out, "Certificates") {
		t.Fatalf("doctor printed neither section, so this run is not the window an absence can be read over:\n%s", out)
	}
	for _, want := range []string{
		"  ✓ something listens on port 80",
		"  ⚠ shop.example.com does not resolve; the record pointing it at 203.0.113.10 is owed",
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
		t.Errorf("exit code = %d over the output above, want 0: a standing check is a report and never a gate, and an owed record is the normal state", code)
	}
	if strings.Contains(out, failGlyph) {
		t.Errorf("doctor refused something on a bootstrapped box whose only finding is an owed record:\n%s", out)
	}
}

func TestRunDoctorWarnsThatNothingRenewsAPinnedWildcardAboutToExpire(t *testing.T) {
	root := healthyProject(t)
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.example.com")
	t.Setenv(clitest.FakeGlobalDomainRenewalEnvVar, "you placed it on this box and you renew it")
	t.Setenv(clitest.FakeGlobalDomainExpiresEnvVar, strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10))

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, root, &stdout, &stderr)
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

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

type terminalAsker struct{}

func (terminalAsker) Attended() bool { return true }

func (terminalAsker) Confirm(context.Context, string) (bool, error) { return true, nil }

func waitForFrame(t *testing.T, terminal *syncBuffer) {
	t.Helper()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if terminal.String() != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the spinner drew nothing, so this run proves nothing about standing it down")
}

func TestDoctorStandsTheSpinnerDownWhileTheHostTrustAsks(t *testing.T) {
	var terminal syncBuffer
	host := provider.Trust{Ask: terminalAsker{}, Out: &terminal}
	spinner := runui.StartSpinner(runui.Presentation{Format: runui.FormatHuman, TTY: true, Width: 80}, &terminal, "Checking your setup")
	t.Cleanup(spinner.Stop)

	trust := runui.TrustFor(host, spinner)
	if trust.Ask != host.Ask || trust.Out != host.Out {
		t.Errorf("the trust asks through %#v on %#v, want the terminal the process was started on", trust.Ask, trust.Out)
	}
	if trust.Suspend == nil {
		t.Fatal("the trust has no way to stand the spinner down while it asks, so the two share the terminal")
	}

	waitForFrame(t, &terminal)

	resume := trust.Suspend()
	terminal.Reset()
	time.Sleep(500 * time.Millisecond)
	if drawn := terminal.String(); drawn != "" {
		t.Errorf("the spinner drew %q over the trust prompt", drawn)
	}

	resume()
	waitForFrame(t, &terminal)
}
