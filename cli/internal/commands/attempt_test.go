package commands

import (
	"context"
	"testing"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"

	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/project"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func TestInsideAJobADeploysSourceIsTheOneTheJobWasStartedWith(t *testing.T) {
	p := ProviderRun{job: &jobcontext.Job{ID: "job_1", Git: &runnerv1.GitContext{Commit: "0123abc", Branch: "feature/login"}}}

	source := p.readSource(context.Background(), t.TempDir())
	if source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || source.GetDirty() {
		t.Errorf("source = %v, want the commit and branch the job was started with", source)
	}
}

func TestInsideAJobADeploymentIsTheOneTheConsoleCreatedForIt(t *testing.T) {
	p := ProviderRun{job: &jobcontext.Job{ID: "job_1", DeploymentID: "0123456789abcdef0123456789abcdef"}}
	if got := p.deploymentID(); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("deployment = %q, want the job's", got)
	}
}

func TestADeploymentOnOcelCloudIsReportedAsMadeByTheProviderOcelCloudServes(t *testing.T) {
	p := ProviderRun{cloud: &CloudTarget{}}

	if got := p.providerName(project.NewUnconfigured(t.TempDir(), "shop")); got != CloudProviderID {
		t.Errorf("provider = %q, want %q for a project that names none and runs on Ocel Cloud", got, CloudProviderID)
	}
}

func TestInsideAJobADeploymentIsReportedAsRunOnARunnerNotFromATerminal(t *testing.T) {
	p := ProviderRun{job: &jobcontext.Job{ID: "job_1"}}
	trigger, ci := p.readTrigger("12")
	if trigger.GetKind() != consolev1.TriggerKind_TRIGGER_KIND_CI || ci.GetName() != "ocel-runner" || ci.GetPr() != 12 {
		t.Fatalf("trigger = %v, ci = %v, want a run on a runner for pull request 12", trigger, ci)
	}
}
