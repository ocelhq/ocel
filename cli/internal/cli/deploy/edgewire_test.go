package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
			root, journal, deps := clitest.SetUpEdgeFixture(t, tc.declaration)

			var stdout, stderr bytes.Buffer
			if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	root, journal, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\", options: { zone: \"acme.com\" } },\n  dns: { kind: \"cloudflare\", zone: \"acme.com\" },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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

	root, _, deps := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeEdgeRefusalEnvVar, refusal)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
