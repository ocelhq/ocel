package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestACommandThatReadsTheBootstrapNamesTheFeatureItLacks(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	deps := newDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr)
	if err == nil {
		t.Fatal("a command reading a bootstrap that lacks a feature this project needs ran on regardless")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap production --features image-optimization,isr") {
		t.Errorf("refusal = %q, want the literal command to run", err)
	}
}

func TestEnvAsksTheBootstrapOnlyWhetherThisCLICanSpeakToIt(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	deps := newDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), deps, root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("a variable this bootstrap holds was refused over a feature no variable needs: %v", err)
	}
}
