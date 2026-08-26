package bootstrap

import (
	"bytes"
	"context"
	"path/filepath"
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

func TestOnlyWhatRendersDependentsPaysForThem(t *testing.T) {
	t.Run("bootstrap reads the catalogue, then plans the apply it is about to send", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")
		journal := describeJournal(t)
		t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.ReadJournal(t, journal)
		if len(got) != 2 {
			t.Fatalf("the provider was asked %d times, want the catalogue and then the plan: %v", len(got), got)
		}
		if !strings.Contains(got[0], "withDependents=true") {
			t.Errorf("bootstrap asked %v; a provider that draws no plan leaves the dependent names nowhere else to come from", got)
		}
		if strings.Contains(got[0], "intent=") || !strings.Contains(got[1], "intent=features=isr,force=false") {
			t.Errorf("the provider was asked %v, want the second ask to carry the apply it would send", got)
		}
	})
}
