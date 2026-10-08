package promotions

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func namingARegistry(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
};
`)
}

func isTheProjectsRegistry(registry *contractv1.ImageRegistry) bool {
	return registry.GetServer() == "registry.example.com" && registry.GetUsername() == "acme-bot" && registry.GetPassword() == "hunter2"
}

func TestPruningPromotionsSendsTheRegistryTheProjectNamesSoTheImagesOfWhatItReclaimsGoWithThem(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	project := promotedThrice(t)
	namingARegistry(t, project)
	invocation := clitest.NewInvocation()
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)

	if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, &stdout, strings.NewReader("")); err != nil {
		t.Fatalf("runPromotionsPrune err = %v; stdout=%s", err, stdout.String())
	}

	reqs := clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, project.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure)
	if len(reqs) != 1 || !isTheProjectsRegistry(reqs[0].GetProjectRegistry()) {
		t.Errorf("the prune sent %d requests, want one naming the project's registry with its secret resolved", len(reqs))
	}
}

func TestPruningPromotionsWhoseRegistryVariableIsUnsetStillPrunesAndSaysWhatItLeaves(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "")
	project := promotedThrice(t)
	namingARegistry(t, project)
	invocation := clitest.NewInvocation()
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)

	if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, &stdout, strings.NewReader("")); err != nil {
		t.Fatalf("runPromotionsPrune err = %v; stdout=%s", err, stdout.String())
	}

	reqs := clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, project.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure)
	if len(reqs) != 1 || reqs[0].GetProjectRegistry() != nil {
		t.Errorf("the prune sent %d requests, want one naming no registry: a token that is gone must not keep promotions from being reclaimed", len(reqs))
	}
	if out := stdout.String(); !strings.Contains(out, "OCEL_TEST_REGISTRY_TOKEN") {
		t.Errorf("stdout = %q, want it to say the images stay and name the unset variable", out)
	}
}

func TestARollbackSendsTheRegistryTheProjectNamesSoTheImagesOfWhatItDropsGoWithThem(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	project := promotedTwice(t)
	namingARegistry(t, project)
	invocation := clitest.NewInvocation()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)

	if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	reqs := clitest.RequestsTo[*contractv1.RollbackRequest](t, project.Requests, contractv1connect.ProviderServiceRollbackProcedure)
	if len(reqs) != 1 || !isTheProjectsRegistry(reqs[0].GetProjectRegistry()) {
		t.Errorf("the rollback sent %d requests, want one naming the project's registry with its secret resolved", len(reqs))
	}
}
