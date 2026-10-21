package commands_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
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

func writeJobContext(t *testing.T, contents string) func(string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{commands.CloudJobContextEnvVar: path, executables.OverrideEnvVar: "/opt/ocel/providers"}
	return func(key string) string { return env[key] }
}

func encodeJobContext(t *testing.T, job *cloudv1.JobContext) string {
	t.Helper()
	encoded, err := protojson.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
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

func TestInsideAnOcelCloudJobARunRefusesAProjectThatIsNotTheJobsAsAnInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		want   string
	}{
		{name: "a config naming a provider", config: `{"slug": "shop", "provider": {"fake": {}}}`, want: `names the provider "fake"`},
		{name: "a config naming another slug", config: `{"slug": "cart"}`, want: "started for the project shop"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invocation := insideCloudJob(t, "shop")
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), tt.config)

			_, err := invocation.ResolveTarget(t.Context(), root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
			if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeProjectInvalidConfig {
				t.Errorf("code = %q, want %q", code, clierror.CodeProjectInvalidConfig)
			}
		})
	}
}

func TestReadBuilderJobReadsNoJobWhenOcelCloudNamesNoContext(t *testing.T) {
	job, err := commands.ReadBuilderJob(func(string) string { return "" })
	if job != nil || err != nil {
		t.Fatalf("job = %+v, err = %v outside a job", job, err)
	}
}

func TestReadBuilderJobReadsTheJobFromTheContextOcelCloudWrites(t *testing.T) {
	getenv := writeJobContext(t, encodeJobContext(t, &cloudv1.JobContext{
		JobId: "job_1", Slug: "shop", PreviewLabel: "pr-12",
		Git: &cloudv1.GitContext{Commit: "0123abc", Branch: "feature/login", Dirty: true, PullRequest: 12},
	}))

	job, err := commands.ReadBuilderJob(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job_1" || job.Slug != "shop" || job.PreviewLabel != "pr-12" || job.ProvidersDir != "/opt/ocel/providers" {
		t.Fatalf("job = %+v, want job_1 for shop labelled pr-12, spawning providers from /opt/ocel/providers", job)
	}
	if source := job.Source(); source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || !source.GetDirty() {
		t.Errorf("source = %v, want the context's commit, branch and dirty state", source)
	}
	if pr := job.DiscoverPRNumber(); pr != "12" {
		t.Errorf("pull request = %q, want the context's", pr)
	}
}

func TestReadBuilderJobRefusesAContextOcelCloudCouldNotHaveWritten(t *testing.T) {
	valid := func() *cloudv1.JobContext { return &cloudv1.JobContext{JobId: "job_1", Slug: "shop"} }
	withContext := func(change func(*cloudv1.JobContext)) func(t *testing.T) func(string) string {
		return func(t *testing.T) func(string) string {
			job := valid()
			change(job)
			return writeJobContext(t, encodeJobContext(t, job))
		}
	}
	for _, tt := range []struct {
		name   string
		getenv func(t *testing.T) func(string) string
		want   string
	}{
		{name: "a context file that is not there", getenv: func(*testing.T) func(string) string {
			env := map[string]string{commands.CloudJobContextEnvVar: "/nonexistent/job.json", executables.OverrideEnvVar: "/opt"}
			return func(key string) string { return env[key] }
		}, want: "/nonexistent/job.json"},
		{name: "a context that is not JSON", getenv: func(t *testing.T) func(string) string { return writeJobContext(t, "job_1") }, want: "not an Ocel Cloud job's context"},
		{name: "a dirty state that is not a boolean", getenv: func(t *testing.T) func(string) string {
			return writeJobContext(t, `{"jobId": "job_1", "slug": "shop", "git": {"dirty": "maybe"}}`)
		}, want: "not an Ocel Cloud job's context"},
		{name: "a context naming no slug", getenv: withContext(func(j *cloudv1.JobContext) { j.Slug = "" }), want: "slug"},
		{name: "a slug with the reserved separator", getenv: withContext(func(j *cloudv1.JobContext) { j.Slug = "my--shop" }), want: "slug"},
		{name: "a preview label that is not a DNS label", getenv: withContext(func(j *cloudv1.JobContext) { j.PreviewLabel = "PR 12" }), want: "preview_label"},
		{name: "a commit that is not a git object name", getenv: withContext(func(j *cloudv1.JobContext) { j.Git = &cloudv1.GitContext{Commit: "main"} }), want: "git.commit"},
		{name: "no providers to spawn", getenv: func(t *testing.T) func(string) string {
			getenv := writeJobContext(t, encodeJobContext(t, valid()))
			return func(key string) string {
				if key == executables.OverrideEnvVar {
					return ""
				}
				return getenv(key)
			}
		}, want: executables.OverrideEnvVar},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job, err := commands.ReadBuilderJob(tt.getenv(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("job = %+v, err = %v, want a refusal naming %q", job, err, tt.want)
			}
			if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeCloudJobContextInvalid {
				t.Errorf("code = %q, want %q", code, clierror.CodeCloudJobContextInvalid)
			}
		})
	}
}

func TestInsideAnOcelCloudJobTheGitContextIsTheOneTheJobWasStartedWith(t *testing.T) {
	job := commands.BuilderJob{ID: "job_1", Git: &cloudv1.GitContext{Commit: "0123abc", Branch: "feature/login", Dirty: true, PullRequest: 12}}

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

func TestInsideAnOcelCloudJobStartedWithNoGitContextNoBranchSourceOrPullRequestIsRead(t *testing.T) {
	job := commands.BuilderJob{ID: "job_1"}

	if source := job.Source(); source != nil {
		t.Errorf("source = %v, want none for a job started outside git", source)
	}
	if _, err := job.ReadGitBranch(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no git branch") {
		t.Errorf("err = %v, want it to say the job names no branch", err)
	}
	if pr := job.DiscoverPRNumber(); pr != "" {
		t.Errorf("pull request = %q, want none", pr)
	}
}
