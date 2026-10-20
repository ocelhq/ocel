package commands_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
)

func insideCloudJob(t *testing.T, slug string) commands.Invocation {
	t.Helper()
	invocation := clitest.NewInvocation()
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		t.Error("read a console login inside an Ocel Cloud job")
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		t.Error("asked a console for Ocel Cloud inside an Ocel Cloud job")
		return nil, nil
	}
	invocation.BuilderJob = &commands.BuilderJob{ID: "job_1", Slug: slug, ProvidersDir: t.TempDir()}
	return invocation
}

func TestInsideAnOcelCloudJobAProjectNamingNoProviderRunsThroughTheProviderOcel(t *testing.T) {
	invocation := insideCloudJob(t, "shop")
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if target.IsCloud() {
		t.Fatal("the target is Ocel Cloud again, which would upload the source from inside its own job")
	}
	if target.Project.Slug != "shop" || !target.Project.HasProvider() || target.Project.Provider.ID != commands.CloudProviderID {
		t.Fatalf("project = %+v, want shop through the provider %s", target.Project, commands.CloudProviderID)
	}

	loaded, err := invocation.LoadProject(t.Context(), root)
	if err != nil || !loaded.HasProvider() || loaded.Provider.ID != commands.CloudProviderID {
		t.Fatalf("loaded %+v, err = %v, want every load to run through the provider %s", loaded, err, commands.CloudProviderID)
	}
}

func TestInsideAnOcelCloudJobADirectoryWithNoConfigRunsAsTheJobsProject(t *testing.T) {
	invocation := insideCloudJob(t, "shop")
	root := filepath.Join(t.TempDir(), "source")
	clitest.WriteFile(t, filepath.Join(root, "package.json"), `{"name": "anything", "scripts": {"start": "node index.js"}}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "")

	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := target.Project
	if target.IsCloud() || cfg.Slug != "shop" || cfg.Dir != root || !cfg.HasProvider() || cfg.Provider.ID != commands.CloudProviderID {
		t.Fatalf("target = %+v, want %s run as shop through the provider %s", target, root, commands.CloudProviderID)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "shop" || cfg.Apps[0].Path != "." {
		t.Fatalf("apps = %+v, want the root app named after the slug", cfg.Apps)
	}
}

func TestInsideAnOcelCloudJobARunRefusesAProjectThatIsNotTheJobs(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		job    func(*commands.BuilderJob)
		want   string
	}{
		{name: "a config naming a provider", config: `{"slug": "shop", "provider": {"fake": {}}}`, want: `names the provider "fake"`},
		{name: "a config naming another slug", config: `{"slug": "cart"}`, want: "started for the project shop"},
		{name: "a job naming no slug", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.Slug = "" }, want: commands.CloudSlugEnvVar},
		{name: "a job with no providers", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.ProvidersDir = "" }, want: "OCEL_PROVIDERS_DIR"},
		{name: "a preview label that is not a DNS label", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.PreviewLabel = "PR 12" }, want: commands.CloudPreviewLabelEnvVar},
		{name: "a commit that is not a git object name", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.GitCommit = "main" }, want: commands.CloudGitCommitEnvVar},
		{name: "a dirty state that is not a boolean", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.GitDirty = "maybe" }, want: commands.CloudGitDirtyEnvVar},
		{name: "a pull request that is not a number", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.GitPullRequest = "twelve" }, want: commands.CloudGitPullRequestEnvVar},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invocation := insideCloudJob(t, "shop")
			if tt.job != nil {
				tt.job(invocation.BuilderJob)
			}
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), tt.config)

			_, err := invocation.ResolveTarget(t.Context(), root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

func TestReadBuilderJobReadsTheJobOnlyWhenOcelCloudSetsOne(t *testing.T) {
	env := map[string]string{}
	if job := commands.ReadBuilderJob(func(key string) string { return env[key] }); job != nil {
		t.Fatalf("job = %+v outside a job", job)
	}
	env = map[string]string{
		commands.CloudJobEnvVar: "job_1", commands.CloudSlugEnvVar: "shop", "OCEL_PROVIDERS_DIR": "/opt/ocel/providers",
		commands.CloudPreviewLabelEnvVar: "pr-12",
		commands.CloudGitCommitEnvVar:    "0123abc", commands.CloudGitBranchEnvVar: "feature/login", commands.CloudGitDirtyEnvVar: "true", commands.CloudGitPullRequestEnvVar: "12",
	}
	job := commands.ReadBuilderJob(func(key string) string { return env[key] })
	want := commands.BuilderJob{
		ID: "job_1", Slug: "shop", ProvidersDir: "/opt/ocel/providers",
		PreviewLabel: "pr-12",
		GitCommit:    "0123abc", GitBranch: "feature/login", GitDirty: "true", GitPullRequest: "12",
	}
	if job == nil || *job != want {
		t.Fatalf("job = %+v, want %+v", job, want)
	}
}

func TestInsideAnOcelCloudJobTheGitContextIsTheOneTheJobWasStartedWith(t *testing.T) {
	job := commands.BuilderJob{ID: "job_1", GitCommit: "0123abc", GitBranch: "feature/login", GitDirty: "true", GitPullRequest: "12"}

	if source := job.Source(); source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || !source.GetDirty() {
		t.Errorf("source = %v, want the job's commit, branch and dirty state", source)
	}
	if branch, err := job.ReadGitBranch(t.TempDir()); err != nil || branch != "feature/login" {
		t.Errorf("branch = %q, %v, want the job's branch", branch, err)
	}
	if pr := job.DiscoverPRNumber(); pr != "12" {
		t.Errorf("pull request = %q, want the job's", pr)
	}
}

func TestInsideAnOcelCloudJobStartedWithNoGitContextNoBranchOrSourceIsRead(t *testing.T) {
	job := commands.BuilderJob{ID: "job_1"}

	if source := job.Source(); source != nil {
		t.Errorf("source = %v, want none for a job started outside git", source)
	}
	if _, err := job.ReadGitBranch(t.TempDir()); err == nil || !strings.Contains(err.Error(), commands.CloudGitBranchEnvVar) {
		t.Errorf("err = %v, want it to say the job names no branch", err)
	}
}
