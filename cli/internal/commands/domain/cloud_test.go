package domain

import (
	"bytes"
	"context"
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
}
