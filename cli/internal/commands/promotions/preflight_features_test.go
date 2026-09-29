package promotions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestACommandThatReadsTheBootstrapNamesTheFeatureItLacks(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	invocation := clitest.NewInvocation()
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	err := runPromotionsList(context.Background(), invocation, root, &stdout, &stderr)
	if err == nil {
		t.Fatal("a command reading a bootstrap that lacks a feature this project needs ran on regardless")
	}
	if out := stdout.String(); !strings.Contains(out, "ocel bootstrap production --features image-optimization,isr") {
		t.Errorf("refusal = %q, want the literal command to run", out)
	}
}
