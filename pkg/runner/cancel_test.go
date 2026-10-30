package runner_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

func TestCancelingARunningJobLetsItsAgentStopTheRunBeforeTheAgentIsRemoved(t *testing.T) {
	r := newTestRunnerTrusting(t, t.TempDir(), newTokens(t), func(c *runner.Config) { c.CancelGrace = time.Hour })
	job := r.startJob(t, time.Now().Add(time.Hour))
	if _, err := job.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{}); err != nil {
		t.Fatal(err)
	}

	canceled, err := job.person.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.launcher.cancels(); len(got) != 0 {
		t.Fatalf("CancelJob() removed the agent %q while it could still stop the run itself", got)
	}
	if canceled.GetJob().GetState() == runnerv1.JobState_JOB_STATE_CANCELED {
		t.Fatalf("CancelJob() answered %v canceled before the agent stopped the run", canceled.GetJob())
	}
	appended, err := job.agent.AppendOutput(t.Context(), &runnerv1.AppendOutputRequest{Lines: []string{"interrupted"}})
	if err != nil || !appended.GetCancel() {
		t.Fatalf("AppendOutput() = %v, %v, want the agent told to stop", appended, err)
	}
	if _, err := job.agent.Finish(t.Context(), &runnerv1.FinishRequest{ExitCode: 130}); err != nil {
		t.Fatal(err)
	}
	if state := job.state(t).GetState(); state != runnerv1.JobState_JOB_STATE_CANCELED {
		t.Errorf("the job is %s once its agent finished, want it canceled", state)
	}
	if got := r.launcher.cancels(); len(got) != 0 {
		t.Errorf("the agent %q was removed after it finished on its own", got)
	}
}

func TestACanceledJobWhoseAgentDoesNotStopWithinTheGraceIsRemovedAndCanceled(t *testing.T) {
	r := newTestRunnerTrusting(t, t.TempDir(), newTokens(t), func(c *runner.Config) { c.CancelGrace = 50 * time.Millisecond })
	job := r.startJob(t, time.Now().Add(time.Hour))
	if _, err := job.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := job.person.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.state(t).GetState() != runnerv1.JobState_JOB_STATE_CANCELED {
		if time.Now().After(deadline) {
			t.Fatalf("the job is %v past the grace, want it canceled", job.state(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.launcher.cancels(); len(got) != 1 {
		t.Errorf("the agent was removed %d times, want once, after the grace", len(got))
	}
}

func TestAStartJobWhoseAgentFailedToLaunchIsStartedAgainWhenResent(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	r.launcher.failing = 1
	job := r.prepareJob(t, time.Now().Add(time.Hour))

	_, err := job.start(t)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("StartJob() while the agent cannot launch error = %v, want %s so the console sends it again", err, connect.CodeUnavailable)
	}
	if _, err := job.person.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: job.id}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetJob() of a job whose agent never launched error = %v, want the job not held", err)
	}
	if _, err := job.start(t); err != nil {
		t.Fatalf("StartJob() resent error = %v, want the job started", err)
	}
	if len(r.launcher.launched) != 1 || job.state(t).GetState() != runnerv1.JobState_JOB_STATE_QUEUED {
		t.Errorf("after the resend the job is %v with %d launches, want it queued and launched once", job.state(t), len(r.launcher.launched))
	}
}

func TestAJobDirectoryACrashLeftWithoutItsRecordIsDroppedOnStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jobs", "6f72672d61", "job_half_written"), 0o700); err != nil {
		t.Fatal(err)
	}
	newTestRunner(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "jobs", "6f72672d61", "job_half_written")); !os.IsNotExist(err) {
		t.Errorf("the half-written job directory is still there: %v", err)
	}
}
