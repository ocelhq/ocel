package root

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func mustNewInvocation(t *testing.T, set *flags) commands.Invocation {
	t.Helper()
	invocation, err := newInvocation(run.NewBus(time.Now), set)
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func TestEveryCommandReportsDeploymentsWithTheSavedConsoleLogin(t *testing.T) {
	invocation := mustNewInvocation(t, &flags{})

	if invocation.Console.LoadCredentials == nil {
		t.Fatal("Console loads no credentials, so every deployment would be reported as from a user who is not logged in")
	}
}

func TestEveryCommandLearnsWhetherTheConsoleOffersOcelCloud(t *testing.T) {
	invocation := mustNewInvocation(t, &flags{})

	if invocation.CloudOffer == nil {
		t.Fatal("no CloudOffer, so a project that names no provider never deploys to Ocel Cloud")
	}
}

func TestInsideAnOcelCloudJobNoCommandReadsAConsoleLoginOrAsksForOcelCloud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, []byte(`{"jobId": "job_1", "slug": "shop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(commands.CloudJobContextEnvVar, path)
	t.Setenv(executables.OverrideEnvVar, t.TempDir())
	invocation := mustNewInvocation(t, &flags{})

	if invocation.BuilderJob == nil || invocation.BuilderJob.ID != "job_1" || invocation.BuilderJob.Slug != "shop" {
		t.Fatalf("builder job = %+v, want job_1 for shop", invocation.BuilderJob)
	}
	if invocation.Console.LoadCredentials != nil || invocation.CloudOffer != nil {
		t.Fatal("a command inside an Ocel Cloud job would report to a console or resolve Ocel Cloud again")
	}
}

func TestInsideAnOcelCloudJobWhoseContextCannotBeReadEveryCommandRefuses(t *testing.T) {
	t.Setenv(commands.CloudJobContextEnvVar, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(executables.OverrideEnvVar, t.TempDir())

	_, stdout, _ := executeAndReportRoot(t, "--json", "deploy")

	if code := requireOneFailureDocument(t, stdout)["code"]; code != clierror.CodeCloudJobContextInvalid {
		t.Errorf("error code = %v, want %s", code, clierror.CodeCloudJobContextInvalid)
	}
}
