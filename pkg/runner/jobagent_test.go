package runner_test

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
)

func (r *testRunner) agentOf(t *testing.T, id string) runnerv1connect.JobAgentServiceClient {
	t.Helper()
	token := r.tokens.job(t, "org-a", endpoint.Job{ID: id, ProjectID: "00000000-0000-4000-8000-000000000001", Runner: "runner-test", Slug: "web", Tier: environment.TierProduction, Expires: time.Now().Add(time.Hour)})
	return runnerv1connect.NewJobAgentServiceClient(http.DefaultClient, r.url, bearing(token))
}

func (r *testRunner) startGitJob(t *testing.T, job started, gitToken string) {
	t.Helper()
	git := &runnerv1.GitSource{CloneUrl: "https://git.example.com/shop/web.git", Commit: strings.Repeat("a", 40), Token: gitToken}
	if _, err := job.jobs.StartJob(t.Context(), &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{
		Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY, Source: &runnerv1.JobRequest_GitSource{GitSource: git},
	}}); err != nil {
		t.Fatal(err)
	}
}

func wantNoFileHolding(t *testing.T, dir, secret, when string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s holds the git token %s, want it gone from the runner's disk", path, when)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

const gitToken = "ghs_test_installation_token_0123456789"

func TestAGitTokenLeavesTheRunnersDiskOnceTheAgentHoldsTheSpec(t *testing.T) {
	dir := t.TempDir()
	r := newTestRunner(t, dir)
	job := r.prepareJob(t, time.Now().Add(time.Hour))
	r.startGitJob(t, job, gitToken)
	r.server.Close()

	again := newTestRunnerTrusting(t, dir, r.tokens)
	agent := again.agentOf(t, job.id)
	spec, err := agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetJob().GetGitSource().GetToken() != gitToken {
		t.Fatalf("GetSpec() after a restart hands the agent git token %q, want the one StartJob was given", spec.GetJob().GetGitSource().GetToken())
	}
	if _, err := agent.AppendOutput(t.Context(), &runnerv1.AppendOutputRequest{}); err != nil {
		t.Fatal(err)
	}
	wantNoFileHolding(t, dir, gitToken, "after the agent reported")
}

func TestAGitTokenLeavesTheRunnersDiskWhenItsJobEndsBeforeItsAgentReports(t *testing.T) {
	dir := t.TempDir()
	r := newTestRunner(t, dir)
	job := r.prepareJob(t, time.Now().Add(time.Hour))
	r.startGitJob(t, job, gitToken)

	if _, err := job.person.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
		t.Fatal(err)
	}
	if state := job.state(t).GetState(); state != runnerv1.JobState_JOB_STATE_CANCELED {
		t.Fatalf("the queued job is %s after CancelJob, want it canceled", state)
	}
	wantNoFileHolding(t, dir, gitToken, "after its job ended")
}
