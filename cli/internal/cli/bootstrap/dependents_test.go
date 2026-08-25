package bootstrap

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func describeJournal(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "describe.journal")
	t.Setenv(clitest.FakeDescribeJournalEnvVar, path)
	return path
}

func TestOnlyTheCommandThatRendersDependentsAsksForThem(t *testing.T) {
	t.Run("status reads the bootstrap and nothing that grows with the account", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		journal := describeJournal(t)
		d := clitest.NewSession()
		clitest.SetLoggedIn(&d)
		clitest.StubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), d, root, StatusOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runBootstrapStatus err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 2 {
			t.Fatalf("the provider was asked %d times, want once per class: %v", len(got), got)
		}
		if slices.ContainsFunc(got, func(line string) bool { return strings.Contains(line, "withDependents=true") }) {
			t.Errorf("status asked %v; it renders no dependent, and reading them costs one query per project in the account", got)
		}
	})

	t.Run("bootstrap asks, because it names who breaks when a feature goes", func(t *testing.T) {
		root, _, d := clitest.SetUpEdgeFixture(t, "")
		journal := describeJournal(t)
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 1 || !strings.Contains(got[0], "withDependents=true") {
			t.Errorf("the provider was asked %v, want the one ask to carry the dependents this command prints", got)
		}
	})
}
