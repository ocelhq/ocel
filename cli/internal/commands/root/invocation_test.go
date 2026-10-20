package root

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func TestEveryCommandReportsDeploymentsWithTheSavedConsoleLogin(t *testing.T) {
	invocation := newInvocation(run.NewBus(time.Now), &flags{})

	if invocation.Console.LoadCredentials == nil {
		t.Fatal("Console loads no credentials, so every deployment would be reported as from a user who is not logged in")
	}
}

func TestEveryCommandLearnsWhetherTheConsoleOffersOcelCloud(t *testing.T) {
	invocation := newInvocation(run.NewBus(time.Now), &flags{})

	if invocation.CloudOffer == nil {
		t.Fatal("no CloudOffer, so a project that names no provider never deploys to Ocel Cloud")
	}
}

func TestInsideAnOcelCloudJobNoCommandReadsAConsoleLoginOrAsksForOcelCloud(t *testing.T) {
	t.Setenv(commands.CloudJobEnvVar, "job_1")
	t.Setenv(commands.CloudSlugEnvVar, "shop")
	invocation := newInvocation(run.NewBus(time.Now), &flags{})

	if invocation.BuilderJob == nil || invocation.BuilderJob.ID != "job_1" || invocation.BuilderJob.Slug != "shop" {
		t.Fatalf("builder job = %+v, want job_1 for shop", invocation.BuilderJob)
	}
	if invocation.Console.LoadCredentials != nil || invocation.CloudOffer != nil {
		t.Fatal("a command inside an Ocel Cloud job would report to a console or resolve Ocel Cloud again")
	}
}
