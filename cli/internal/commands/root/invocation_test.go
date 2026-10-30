package root

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/runner"
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

func TestInsideAJobNoCommandReadsAConsoleLoginOrAsksForOcelCloudAndEveryOneReportsWithTheJobToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.json")
	context := `{"jobId": "job_1", "projectId": "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", "tier": "TIER_PRODUCTION", "slug": "shop", "consoleUrl": "https://console.example.com", "jobTokenFile": "/workspace/job/token"}`
	if err := os.WriteFile(path, []byte(context), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runner.ContextEnvVar, path)
	invocation := mustNewInvocation(t, &flags{})

	if job := invocation.Job(); job == nil || job.ID != "job_1" || job.Slug != "shop" {
		t.Fatalf("job = %+v, want job_1 for shop", job)
	}
	if invocation.Console.LoadCredentials != nil || invocation.CloudOffer != nil {
		t.Fatal("a command inside a job would read a console login or resolve Ocel Cloud again")
	}
	if invocation.Console.Context != invocation.JobContext || invocation.ConsoleCredential.Context != invocation.JobContext || invocation.Job().Input == nil {
		t.Fatal("a command inside a job would report to the console or ask it for credentials without its job token, or answer no input")
	}
}

func TestInsideAJobWhoseContextCannotBeReadEveryCommandRefuses(t *testing.T) {
	t.Setenv(runner.ContextEnvVar, filepath.Join(t.TempDir(), "missing.json"))

	_, stdout, _ := executeAndReportRoot(t, "--json", "deploy")

	if code := requireOneFailureDocument(t, stdout)["code"]; code != clierror.CodeJobContextInvalid {
		t.Errorf("error code = %v, want %s", code, clierror.CodeJobContextInvalid)
	}
}
