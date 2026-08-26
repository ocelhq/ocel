package doctor

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
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
	project.warn("no preview domain declared", "add `domains: { preview: \"*.preview.example.com\" }` to your config")
	found.add(project)

	edge := section{name: "Cloudflare"}
	edge.fail("CLOUDFLARE_API_TOKEN rejected", "create a token with the scopes from `ocel permissions deploy`")
	found.add(edge)

	preview := section{name: "Preview"}
	preview.neutral("not set up — run `ocel bootstrap preview` to add previews")
	found.add(preview)

	var out bytes.Buffer
	found.render(&out, newPaint(&out))

	want := strings.Join([]string{
		"Project  my-shop · ocel.config.ts",
		"  ✓ config loads — 2 apps (web, api)",
		"  ⚠ no preview domain declared",
		"    → add `domains: { preview: \"*.preview.example.com\" }` to your config",
		"",
		"Cloudflare",
		"  ✗ CLOUDFLARE_API_TOKEN rejected",
		"    → create a token with the scopes from `ocel permissions deploy`",
		"",
		"Preview",
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
    { name: "web", path: "apps/web", framework: "express" },
    { name: "api", path: "apps/api", framework: "express" },
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
		"AWS  123456789012 · region eu-west-1 · profile shop",
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
		"AWS  123456789012 · region eu-west-1 · profile shop\n  ✓ credentials valid",
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
		"  ⚠ no preview domain declared",
		"Preview\n  – not set up — run `ocel bootstrap preview` to add previews\n",
		"1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}
