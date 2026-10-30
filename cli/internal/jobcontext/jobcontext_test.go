package jobcontext_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

const projectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

func writeJobContext(t *testing.T, contents string) func(string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{runner.ContextEnvVar: path}
	return func(key string) string { return env[key] }
}

func encode(t *testing.T, job *runnerv1.JobContext) string {
	t.Helper()
	encoded, err := protojson.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func validJob() *runnerv1.JobContext {
	return &runnerv1.JobContext{
		JobId: "job_1", ProjectId: projectID, Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop",
		ConsoleUrl: "https://console.example.com", JobTokenFile: "/workspace/job/token",
	}
}

func TestReadReadsNoContextOutsideAJobOrAPipeline(t *testing.T) {
	read, err := jobcontext.Read(func(string) string { return "" })
	if read != nil || err != nil {
		t.Fatalf("context = %+v, err = %v, want none", read, err)
	}
}

func TestReadReadsAJobFromTheContextItsRunnerWrites(t *testing.T) {
	job := validJob()
	job.Tier, job.PreviewLabel, job.DeploymentId = environmentv1.Tier_TIER_PREVIEW, "pr-12", "0123456789abcdef0123456789abcdef"
	job.Git = &runnerv1.GitContext{Commit: "0123abc", Branch: "feature/login", Dirty: true, PullRequest: 12}

	read, err := jobcontext.Read(writeJobContext(t, encode(t, job)))
	if err != nil {
		t.Fatal(err)
	}
	if read.ConsoleURL != "https://console.example.com" || read.ProjectID != projectID || read.Credential != (jobcontext.JobToken{File: "/workspace/job/token"}) {
		t.Fatalf("context = %+v, want the console, the project and the job token as its credential", read)
	}
	got := read.Job
	if got.ID != "job_1" || got.Slug != "shop" || got.PreviewLabel != "pr-12" || got.Tier != environmentv1.Tier_TIER_PREVIEW || got.DeploymentID != job.DeploymentId {
		t.Fatalf("job = %+v, want job_1 for shop's preview labelled pr-12 with its deployment", got)
	}
	if source := got.Source(); source.GetCommit() != "0123abc" || source.GetBranch() != "feature/login" || !source.GetDirty() {
		t.Errorf("source = %v, want the context's commit, branch and dirty state", source)
	}
	if branch, err := got.ReadGitBranch(t.Context(), t.TempDir()); err != nil || branch != "feature/login" {
		t.Errorf("branch = %q, %v, want the job's branch", branch, err)
	}
	if pr := got.DiscoverPRNumber(); pr != "12" {
		t.Errorf("pull request = %q, want the context's", pr)
	}
}

func TestAJobStartedWithNoGitContextReadsNoBranchSourceOrPullRequest(t *testing.T) {
	job := jobcontext.Job{ID: "job_1"}
	if source := job.Source(); source != nil {
		t.Errorf("source = %v, want none", source)
	}
	if _, err := job.ReadGitBranch(t.Context(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "no git branch") {
		t.Errorf("err = %v, want it to say the job names no branch", err)
	}
	if pr := job.DiscoverPRNumber(); pr != "" {
		t.Errorf("pull request = %q, want none", pr)
	}
}

func TestReadRefusesAContextNoRunnerCouldHaveWritten(t *testing.T) {
	withContext := func(change func(*runnerv1.JobContext)) func(t *testing.T) func(string) string {
		return func(t *testing.T) func(string) string {
			job := validJob()
			change(job)
			return writeJobContext(t, encode(t, job))
		}
	}
	for _, tt := range []struct {
		name   string
		getenv func(t *testing.T) func(string) string
		want   string
	}{
		{name: "a context file that is not there", getenv: func(*testing.T) func(string) string {
			return func(key string) string {
				if key == runner.ContextEnvVar {
					return "/nonexistent/job.json"
				}
				return ""
			}
		}, want: "/nonexistent/job.json"},
		{name: "a context that is not JSON", getenv: func(t *testing.T) func(string) string { return writeJobContext(t, "job_1") }, want: "not a job's context"},
		{name: "a context naming no slug", getenv: withContext(func(j *runnerv1.JobContext) { j.Slug = "" }), want: "slug"},
		{name: "a preview label that is not a DNS label", getenv: withContext(func(j *runnerv1.JobContext) { j.PreviewLabel = "PR 12" }), want: "preview_label"},
		{name: "a commit that is not a git object name", getenv: withContext(func(j *runnerv1.JobContext) { j.Git = &runnerv1.GitContext{Commit: "main"} }), want: "git.commit"},
		{name: "no job token file", getenv: withContext(func(j *runnerv1.JobContext) { j.JobTokenFile = "" }), want: "job_token_file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			read, err := jobcontext.Read(tt.getenv(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("context = %+v, err = %v, want a refusal naming %q", read, err, tt.want)
			}
			if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeJobContextInvalid {
				t.Errorf("code = %q, want %q", code, clierror.CodeJobContextInvalid)
			}
		})
	}
}

func TestReadReadsAGitHubActionsPipelineThatNamesAConsoleProject(t *testing.T) {
	env := map[string]string{"GITHUB_ACTIONS": "true", console.ProjectIDEnvVar: projectID, console.URLEnvVar: "https://console.example.com/"}
	read, err := jobcontext.Read(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if read == nil || read.Job != nil || read.ProjectID != projectID || read.ConsoleURL != "https://console.example.com" {
		t.Fatalf("context = %+v, want a pipeline of the named project", read)
	}
	if _, isGitHub := read.Credential.(jobcontext.GitHubActions); !isGitHub {
		t.Fatalf("credential = %T, want GitHub's OIDC token", read.Credential)
	}

	delete(env, console.ProjectIDEnvVar)
	if read, err := jobcontext.Read(func(key string) string { return env[key] }); read != nil || err != nil {
		t.Fatalf("context = %+v, err = %v, want none for a pipeline that names no console project", read, err)
	}
}

func TestAJobTokenIsReadFromItsFileAndNamesItsSubject(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"org:o:tier:production:project:p","aud":"ocel-job"}`))
	if err := os.WriteFile(file, []byte("e30."+payload+".c2ln\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := jobcontext.JobToken{File: file}.Token(t.Context(), "ignored")
	if err != nil || token != "e30."+payload+".c2ln" {
		t.Fatalf("token = %q, err = %v, want the file's token", token, err)
	}
	subject, err := jobcontext.JobToken{File: file}.ReadSubject(t.Context())
	if err != nil || subject != "org:o:tier:production:project:p" {
		t.Fatalf("subject = %q, err = %v, want the token's sub", subject, err)
	}

	if err := os.WriteFile(file, []byte("not-a-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (jobcontext.JobToken{File: file}).ReadSubject(t.Context()); err == nil {
		t.Fatal("read a subject from a token that is not a JWT")
	}
}
