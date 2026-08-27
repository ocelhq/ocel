package deploy

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestDeployYesNeverStopsToAsk(t *testing.T) {
	root, journal, deps := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")
	deps.StdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("an unattended deploy against a bootstrap missing a feature it needs was allowed through")
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features image-optimization,isr` and try again") {
		t.Errorf("stdout = %q, want the literal command to run", stdout.String())
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Errorf("the provider was reached; --yes answers questions about the deploy, it does not order a bootstrap")
	}
}

func TestDeployWithoutATerminalRefusesTheBootstrapItCannotOffer(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts deployOptions
	}{
		{name: "without --yes", opts: deployOptions{}},
		{name: "with --yes", opts: deployOptions{yes: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, journal, deps := clitest.SetUpEdgeFixture(t, "")
			t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

			var stdout, stderr bytes.Buffer
			err := runDeploy(context.Background(), deps, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
			}
			if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features image-optimization,isr` and try again") {
				t.Errorf("stdout = %q, want the literal command to run", stdout.String())
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Error("the provider was reached; a bootstrap nobody can be offered is never ordered, --yes or not")
			}
		})
	}
}
