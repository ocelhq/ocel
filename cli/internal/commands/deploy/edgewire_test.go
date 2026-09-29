package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= "},
		{"a declared api-gateway edge names it", "  edge: \"api-gateway\",\n", "kind=api-gateway"},
		{"a declared relay edge names it", "  edge: \"relay\",\n", "kind=relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := clitest.SetUpEdgeFixture(t, tc.declaration)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, clitest.UsageMonorepoFunctions())

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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

func TestDeploySendsTheEdgeSettingsUnchanged(t *testing.T) {
	root, journal := clitest.SetUpEdgeFixture(t, "  edge: \"relay\",\n  dns: { zone: { zone: \"acme.com\" } },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
	}
	for _, want := range []string{"dns=zone/acme.com", "allowDegraded=streaming,edge-cache"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("provider saw %q, want it to include %q", got[0], want)
		}
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "alb" edge; it supports api-gateway, relay, direct`

	root, _ := clitest.SetUpEdgeFixture(t, "")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	t.Setenv(clitest.FakeEdgeRefusalEnvVar, refusal)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the refused edge to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, refusal) {
		t.Errorf("rendered output = %q, want it to include %q", rendered, refusal)
	}
	if strings.Contains(rendered, "connection lost") {
		t.Errorf("rendered output = %q, want a refusal not to read as a lost connection", rendered)
	}
}
