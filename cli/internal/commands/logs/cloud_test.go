package logs

import (
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func onOcelCloud(t *testing.T) (clitest.CloudProject, func(io.Writer) Dependencies) {
	t.Helper()
	project := clitest.SetUpCloudProject(t)
	recordLogs(t, project.FakeProject)
	return project, func(stderr io.Writer) Dependencies {
		dependencies := plainDependencies(stderr)
		project.LogIn(&dependencies.Invocation)
		return dependencies
	}
}

func TestLogsReadsAndTailsTheAppsOcelCloudRunsThroughTheProviderItServes(t *testing.T) {
	t.Run("history", func(t *testing.T) {
		project, dependencies := onOcelCloud(t)
		got := runWith(project.FakeProject, dependencies, defaultLogsOptions())
		if got.err != nil || !strings.Contains(got.stdout, "web is listening") || !strings.Contains(got.stdout, "api is listening") {
			t.Fatalf("err = %v, stdout = %q, stderr = %q, want the live releases' logs", got.err, got.stdout, got.stderr)
		}
		if len(project.Remote.Calls()) == 0 {
			t.Fatal("Ocel Cloud's provider was never called")
		}
	})

	t.Run("tail", func(t *testing.T) {
		project, dependencies := onOcelCloud(t)
		run := startTail(t, project.FakeProject, dependencies, nil)
		run.waitFor(run.stdout, "api is listening")
		run.waitForTailOpened(project.FakeProject)
		project.Provider.FakeLogs().Append("web-live", liveEntry("a live request"))
		run.waitFor(run.stdout, "a live request")
		if err := run.interrupt(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}
