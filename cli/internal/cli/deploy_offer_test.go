package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestDeployOffersToBootstrapTheExactSet(t *testing.T) {
	root, journal, sess := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")
	sess.StdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), sess, root, deployOptions{}, &stdout, &stderr, &lineByLine{lines: []string{"y\n", "y\n"}}); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 2 {
		t.Fatalf("the provider was reached %d times, want a bootstrap and then the deploy: %v", len(got), got)
	}
	if got[0] != "features=image-optimization,isr force=false" {
		t.Errorf("bootstrap ran with %q, want the set already there plus what is missing", got[0])
	}
	if !strings.Contains(stdout.String(), "add image-optimization") {
		t.Errorf("stdout = %q, want the offer to name what it would add", stdout.String())
	}
}

func TestDeployYesNeverStopsToAsk(t *testing.T) {
	root, journal, sess := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")
	sess.StdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), sess, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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

type lineByLine struct{ lines []string }

func (r *lineByLine) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.lines[0])
	r.lines = r.lines[1:]
	return n, nil
}
