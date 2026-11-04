package domain

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestDomainLsReadsTheHostnamesOcelCloudServesThroughTheProviderItServes(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	invocation := newTestInvocation()
	project.LogIn(&invocation)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{}, &stdout); err != nil {
		t.Fatalf("err = %v; stderr=%s", err, stderr.String())
	}
	if len(project.Remote.Calls()) == 0 {
		t.Fatal("Ocel Cloud's provider was never called")
	}
	requireTokenScopes(t, project.Cloud, "apps.read")
}

func requireTokenScopes(t *testing.T, cloud *clitest.FakeConsole, want ...string) {
	t.Helper()
	if len(cloud.TokenScopes) == 0 || slices.ContainsFunc(cloud.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
		t.Fatalf("tokens issued with scopes %v, want each with exactly %v", cloud.TokenScopes, want)
	}
}
