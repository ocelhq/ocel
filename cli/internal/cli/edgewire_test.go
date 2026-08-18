package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func writeEdgeConfig(t *testing.T, root, declaration string) {
	t.Helper()

	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
`+declaration+`};
`)
}

func readEdgeJournal(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edge journal: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func setUpEdgeFixture(t *testing.T, declaration string) (root, journal string, d deps) {
	t.Helper()

	root, _ = setUpDeployFixture(t)
	writeUsageMonorepo(t, root)
	writeEdgeConfig(t, root, declaration)

	journal = filepath.Join(t.TempDir(), "edge.journal")
	t.Setenv(fakeEdgeJournalEnvVar, journal)

	d = defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	return root, journal, d
}

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge asks for the native edge", "", "kind=native"},
		{"`edge: false` asks for no edge at all", "  edge: false,\n", "kind=none"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := setUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := readEdgeJournal(t, journal)
			if len(got) != 1 {
				t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
			}
			if !strings.Contains(got[0], tc.want) {
				t.Errorf("provider saw %q, want %q", got[0], tc.want)
			}
		})
	}
}

func TestDeployCarriesTheEdgeSettingsUnchanged(t *testing.T) {
	root, journal, d := setUpEdgeFixture(t, "  edge: { kind: \"cloudflare\", options: { zone: \"acme.com\" } },\n  dns: { kind: \"cloudflare\", zone: \"acme.com\" },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := readEdgeJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
	}
	for _, want := range []string{`options={"zone":"acme.com"}`, "dns=cloudflare/acme.com", "allowDegraded=streaming,edge-cache"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("provider saw %q, want it to carry %q", got[0], want)
		}
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "native" edge; it supports cloudflare`

	root, _, d := setUpEdgeFixture(t, "")
	t.Setenv(fakeEdgeRefusalEnvVar, refusal)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the refused edge to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, refusal) {
		t.Errorf("rendered output = %q, want it to carry %q", rendered, refusal)
	}
	if strings.Contains(rendered, "connection lost") {
		t.Errorf("rendered output = %q, want a refusal not to read as a lost connection", rendered)
	}
}

func TestBootstrapSendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge asks for the native edge", "", "kind=native"},
		{"`edge: false` asks for no edge at all", "  edge: false,\n", "kind=none"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := setUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			if err := runBootstrap(context.Background(), d, root, bootstrapOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := readEdgeJournal(t, journal)
			if len(got) != 1 {
				t.Fatalf("bootstrap reached the provider %d times, want exactly 1: %v", len(got), got)
			}
			if !strings.Contains(got[0], tc.want) {
				t.Errorf("provider saw %q, want %q", got[0], tc.want)
			}
		})
	}
}

func TestBootstrapCarriesTheEdgeSettingsUnchanged(t *testing.T) {
	root, journal, d := setUpEdgeFixture(t, "  edge: { kind: \"cloudflare\", options: {} },\n  dns: { kind: \"cloudflare\", zone: \"acme.com\" },\n  allowDegraded: [\"streaming\"],\n")

	var stdout, stderr bytes.Buffer
	if err := runBootstrap(context.Background(), d, root, bootstrapOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := readEdgeJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("bootstrap reached the provider %d times, want exactly 1: %v", len(got), got)
	}
	for _, want := range []string{"kind=cloudflare", "dns=cloudflare/acme.com", "allowDegraded=streaming"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("provider saw %q, want it to carry %q", got[0], want)
		}
	}
}

func TestBootstrapDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge asks for the native edge", "", "kind=native"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := setUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			opts := bootstrapOptions{destroy: true, yes: true}
			if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := readEdgeJournal(t, journal)
			if len(got) != 2 {
				t.Fatalf("destroy reached the provider %d times, want the plan and the teardown: %v", len(got), got)
			}
			for _, line := range got {
				if !strings.Contains(line, tc.want) {
					t.Errorf("provider saw %q, want %q", line, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the "+strings.TrimPrefix(tc.want, "kind=")+" edge") {
				t.Errorf("stdout = %q, want the plan to name the edge it planned", stdout.String())
			}
		})
	}
}
