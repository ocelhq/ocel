package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/removalplan"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= "},
		{"a declared api-gateway edge names it", "  edge: { kind: \"api-gateway\", options: {} },\n", "kind=api-gateway"},
		{"an edge this CLI has never heard of is forwarded whole", "  edge: { kind: \"fastly\", options: {} },\n", "kind=fastly"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := clitest.SetUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
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
	root, journal, d := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\", options: { zone: \"acme.com\" } },\n  dns: { kind: \"cloudflare\", zone: \"acme.com\" },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
	}
	for _, want := range []string{"dns=cloudflare/acme.com", "allowDegraded=streaming,edge-cache"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("provider saw %q, want it to carry %q", got[0], want)
		}
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "fastly" edge; it supports cloudflare`

	root, _, d := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeEdgeRefusalEnvVar, refusal)

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

func TestBootstrapCarriesTheFeatureSetAndNoEdge(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		features    string
		want        string
	}{
		{"a named set reaches the provider whole", "  edge: { kind: \"cloudflare\", options: {} },\n", "isr,image-optimization", "features=isr,image-optimization force=false"},
		{"all names every feature the provider offers", "", "all", "features=isr,image-optimization,cloudflare-edge force=false"},
		{"none leaves the core alone", "", "none", "features= force=false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := clitest.SetUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			opts := bootstrap.Options{Yes: true, Features: tc.features, Declared: true}
			if err := bootstrap.Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 1 {
				t.Fatalf("bootstrap reached the provider %d times, want exactly 1: %v", len(got), got)
			}
			if got[0] != tc.want {
				t.Errorf("provider saw %q, want %q", got[0], tc.want)
			}
			if strings.Contains(got[0], "kind=") {
				t.Errorf("provider saw %q; bootstrap no longer carries an edge, the cloudflare-edge feature does", got[0])
			}
		})
	}
}

func TestBootstrapWithoutTheFlagKeepsWhatIsThere(t *testing.T) {
	root, journal, d := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

	var stdout, stderr bytes.Buffer
	if err := bootstrap.Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, bootstrap.Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 || got[0] != "features=isr force=false" {
		t.Errorf("provider saw %v, want the set the account already carries", got)
	}
}

func TestBootstrapRefusesADropItCannotAskAbout(t *testing.T) {
	root, journal, d := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")

	var stdout, stderr bytes.Buffer
	opts := bootstrap.Options{Yes: true, Features: "isr", Declared: true}
	err := bootstrap.Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runBootstrap err = nil, want the unattended drop refused; stdout=%s", stdout.String())
	}
	for _, want := range []string{"image-optimization", "--force"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the refusal to name %q", stdout.String(), want)
		}
	}
	if _, statErr := os.Stat(journal); statErr == nil {
		t.Errorf("the provider was reached despite the refusal: %v", clitest.ReadJournal(t, journal))
	}
}

func TestBootstrapForcesADropWhenTold(t *testing.T) {
	root, journal, d := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr,image-optimization")

	var stdout, stderr bytes.Buffer
	opts := bootstrap.Options{Yes: true, Features: "isr", Declared: true, Force: true}
	if err := bootstrap.Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 || got[0] != "features=isr force=true" {
		t.Errorf("provider saw %v, want the forced drop carried through", got)
	}
}

func TestBootstrapDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
		planned     string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= ", "cloudfront"},
		{"a declared api-gateway edge names it", "  edge: { kind: \"api-gateway\", options: {} },\n", "kind=api-gateway", "api-gateway"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare", "cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := clitest.SetUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			opts := bootstrap.Options{Yes: true}
			if err := bootstrap.RunDestroy(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 2 {
				t.Fatalf("destroy reached the provider %d times, want the plan and the teardown: %v", len(got), got)
			}
			for _, line := range got {
				if !strings.Contains(line, tc.want) {
					t.Errorf("provider saw %q, want %q", line, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the "+tc.planned+" edge") {
				t.Errorf("stdout = %q, want the plan to name the edge it planned", stdout.String())
			}
		})
	}
}

func TestDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
		planned     string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= ", "cloudfront"},
		{"a declared api-gateway edge names it", "  edge: { kind: \"api-gateway\", options: {} },\n", "kind=api-gateway", "api-gateway"},
		{"an edge this CLI has never heard of is forwarded whole", "  edge: { kind: \"fastly\", options: {} },\n", "kind=fastly", "fastly"},
		{"a declared cloudflare edge names it", "  edge: { kind: \"cloudflare\", options: {} },\n", "kind=cloudflare", "cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, d := clitest.SetUpEdgeFixture(t, tc.declaration)
			t.Setenv(clitest.FakeInfraClassEnvVar, "production")
			t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
			t.Setenv(removalplan.BypassEnv, "test-app")

			var stdout, stderr bytes.Buffer
			if err := runDestroy(context.Background(), d, root, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 2 {
				t.Fatalf("destroy reached the provider %d times, want the plan and the teardown: %v", len(got), got)
			}
			for _, line := range got {
				if !strings.Contains(line, tc.want) {
					t.Errorf("provider saw %q, want %q", line, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the "+tc.planned+" edge") {
				t.Errorf("stdout = %q, want the plan to name the edge it planned", stdout.String())
			}
		})
	}
}
