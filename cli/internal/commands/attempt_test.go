package commands

import "testing"

func TestInsideAnOcelCloudJobADeploysSourceIsTheOneTheJobWasStartedWith(t *testing.T) {
	p := ProviderRun{job: &BuilderJob{ID: "job_1", GitCommit: "0123abc", GitBranch: "feature/login", GitDirty: "false"}}

	source := p.readSource(t.TempDir())
	if source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || source.GetDirty() {
		t.Errorf("source = %v, want the commit and branch the job was started with", source)
	}
}
