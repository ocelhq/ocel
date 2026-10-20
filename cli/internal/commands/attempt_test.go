package commands

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestInsideAnOcelCloudJobADeploysSourceIsTheOneTheJobWasStartedWith(t *testing.T) {
	p := ProviderRun{job: &BuilderJob{ID: "job_1", GitCommit: "0123abc", GitBranch: "feature/login", GitDirty: "false"}}

	source := p.readSource(t.TempDir())
	if source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || source.GetDirty() {
		t.Errorf("source = %v, want the commit and branch the job was started with", source)
	}
}

func TestADeploymentOnOcelCloudIsReportedAsMadeByTheProviderOcelCloudServes(t *testing.T) {
	p := ProviderRun{cloud: &CloudTarget{}}

	if got := p.providerName(project.NewUnconfigured(t.TempDir(), "shop")); got != CloudProviderID {
		t.Errorf("provider = %q, want %q for a project that names none and runs on Ocel Cloud", got, CloudProviderID)
	}
}
