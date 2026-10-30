package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"

	"connectrpc.com/connect"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/runner"
)

func bearing(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

type started struct {
	id     string
	source string
	person runnerv1connect.RunnerServiceClient
	agent  runnerv1connect.JobAgentServiceClient
	jobs   runnerv1connect.RunnerServiceClient
}

func (s started) start(t *testing.T) (*runnerv1.StartJobResponse, error) {
	t.Helper()
	return s.jobs.StartJob(t.Context(), &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, Source: &runnerv1.JobRequest_SourceId{SourceId: s.source}}})
}

func (r *testRunner) startJob(t *testing.T, expires time.Time) started {
	t.Helper()
	job := r.prepareJob(t, expires)
	if _, err := job.start(t); err != nil {
		t.Fatal(err)
	}
	return job
}

func (r *testRunner) prepareJob(t *testing.T, expires time.Time) started {
	t.Helper()
	return r.prepareJobNamed(t, "job_"+strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))[:20], expires)
}

func (r *testRunner) prepareJobNamed(t *testing.T, id string, expires time.Time) started {
	t.Helper()
	person := r.tokens.person(t, "org-a")
	uploads := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, r.url, bearing(person))
	body := "export {}\n"
	missing, err := uploads.ListMissingBlobs(t.Context(), &runnerv1.ListMissingBlobsRequest{Digests: []string{digest(body)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.GetUploads()) == 1 {
		put(t, missing.GetUploads()[0].GetUrl(), body)
	}
	committed, err := uploads.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{{Path: "index.ts", Digest: digest(body), Size: uint64(len(body))}}}})
	if err != nil {
		t.Fatal(err)
	}
	token := r.tokens.job(t, "org-a", endpoint.Job{ID: id, ProjectID: "00000000-0000-4000-8000-000000000001", Runner: "runner-test", Slug: "web", Tier: environment.TierProduction, Expires: expires})
	jobs := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, r.url, bearing(token))
	return started{id: id, source: committed.GetSourceId(), person: uploads, agent: runnerv1connect.NewJobAgentServiceClient(http.DefaultClient, r.url, bearing(token)), jobs: jobs}
}

func (s started) state(t *testing.T) *runnerv1.Job {
	t.Helper()
	read, err := s.person.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: s.id})
	if err != nil {
		t.Fatal(err)
	}
	return read.GetJob()
}

func put(t *testing.T, target, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestTheLauncherIsHandedTheJobTokenTheJobWasStartedWithAndItsExpiry(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	job := r.startJob(t, expires)

	launched := r.launcher.launched[0]
	if launched.JobID != job.id || launched.OrganizationID != "org-a" || !launched.Deadline.Equal(expires) {
		t.Errorf("launched %+v, want job %s of org-a until %s", launched, job.id, expires)
	}
	if _, known := r.tokens.granted[launched.Token]; !known {
		t.Errorf("the agent was handed %q, not the job token StartJob bore", launched.Token)
	}
}

func TestASweepEndsAJobPastItsTokensExpiryAndOneWhoseAgentNeverStarted(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	now := time.Now()
	running := r.startJob(t, now.Add(time.Hour))
	if _, err := running.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{}); err != nil {
		t.Fatal(err)
	}
	r.now.set(now.Add(time.Hour + time.Second))
	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if job := running.state(t); job.GetState() != runnerv1.JobState_JOB_STATE_FAILED || job.GetError().GetCode() != runner.CodeTimeout {
		t.Errorf("a job past its token's expiry is %v, want it failed as timed out", job)
	}
	if _, err := running.agent.AppendOutput(t.Context(), &runnerv1.AppendOutputRequest{Lines: []string{"late"}}); err != nil {
		t.Fatal(err)
	}
}

func TestASweepFailsAJobWhoseAgentNeverStarted(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	now := time.Now()
	queued := r.startJob(t, now.Add(time.Hour))
	r.now.set(now.Add(11 * time.Minute))
	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if job := queued.state(t); job.GetState() != runnerv1.JobState_JOB_STATE_FAILED || job.GetError().GetCode() != runner.CodeInterrupted {
		t.Errorf("a job whose agent never started is %v, want it failed as interrupted", job)
	}
}

func TestASweepStopsAnEndedJobsAgentAgainUntilItStopsAndKeepsItsSlotTakenMeanwhile(t *testing.T) {
	r := newTestRunnerTrusting(t, t.TempDir(), newTokens(t), func(c *runner.Config) { c.MaxJobs = 1 })
	r.launcher.failingCancels = 1
	now := time.Now()
	silent := r.startJob(t, now.Add(time.Hour))
	if _, err := silent.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{}); err != nil {
		t.Fatal(err)
	}
	r.now.set(now.Add(3 * time.Minute))
	if err := r.server.Sweep(t.Context()); err == nil {
		t.Fatal("Sweep() = nil, want the failed stop of the agent reported")
	}
	if state := silent.state(t).GetState(); state != runnerv1.JobState_JOB_STATE_FAILED {
		t.Fatalf("the silent job is %s, want it failed", state)
	}
	next := r.prepareJobNamed(t, "job_next", now.Add(time.Hour))
	if _, err := next.start(t); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("StartJob() while the ended job's agent still runs error = %v, want %s", err, connect.CodeResourceExhausted)
	}

	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.launcher.cancels(); len(got) != 2 || got[1] != got[0] {
		t.Fatalf("the agents removed are %q, want the silent job's twice", got)
	}
	if _, err := next.start(t); err != nil {
		t.Errorf("StartJob() once the ended job's agent stopped error = %v, want the job started", err)
	}
	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.launcher.cancels(); len(got) != 2 {
		t.Errorf("the agents removed are %q, want no stop once the agent stopped", got)
	}
}

func TestARunnerKeepsAJobIDUntilPastItsTokensExpirySoAReplayStartsNoSecondJob(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	now := time.Now()
	job := r.startJob(t, now.Add(time.Hour))
	if _, err := job.person.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
		t.Fatal(err)
	}
	r.now.set(now.Add(50 * time.Minute))
	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	replayed, err := job.jobs.StartJob(t.Context(), &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, Source: &runnerv1.JobRequest_SourceId{SourceId: strings.Repeat("0", 64)}}})
	if err != nil || replayed.GetJob().GetState() != runnerv1.JobState_JOB_STATE_CANCELED {
		t.Fatalf("a replayed StartJob = %v, %v, want the canceled job answered, never a second", replayed.GetJob(), err)
	}
	if len(r.launcher.launched) != 1 {
		t.Errorf("the job's agent was launched %d times, want once", len(r.launcher.launched))
	}

	r.now.set(now.Add(time.Hour + 25*time.Hour))
	if err := r.server.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := job.person.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: job.id}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("GetJob() a day past the token's expiry = %v, want the job forgotten", err)
	}
}

func TestARestartedRunnerHoldsItsJobsAndTheirEvents(t *testing.T) {
	dir := t.TempDir()
	r := newTestRunner(t, dir)
	job := r.startJob(t, time.Now().Add(time.Hour))
	if _, err := job.agent.AppendOutput(t.Context(), &runnerv1.AppendOutputRequest{Lines: []string{"one", "two"}}); err != nil {
		t.Fatal(err)
	}
	r.server.Close()

	again := newTestRunnerTrusting(t, dir, r.tokens)
	person := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, again.url, bearing(r.tokens.person(t, "org-a")))
	page, err := person.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.GetEvents()) != 2 || page.GetJob().GetState() != runnerv1.JobState_JOB_STATE_RUNNING {
		t.Errorf("after a restart the job is %v with events %v, want it running with both lines", page.GetJob(), page.GetEvents())
	}
}

func TestABlobURLWhoseSignatureOrExpiryIsNotTheRunnersIsRefused(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	person := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, r.url, bearing(r.tokens.person(t, "org-a")))
	body := "secret source\n"
	missing, err := person.ListMissingBlobs(t.Context(), &runnerv1.ListMissingBlobsRequest{Digests: []string{digest(body)}})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := url.Parse(missing.GetUploads()[0].GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	forged := *signed
	query := forged.Query()
	query.Set("signature", strings.Repeat("0", 64))
	forged.RawQuery = query.Encode()
	if status := put(t, forged.String(), body); status != http.StatusForbidden {
		t.Errorf("a PUT with a forged signature answered %d, want %d", status, http.StatusForbidden)
	}
	r.now.set(time.Now().Add(2 * time.Hour))
	if status := put(t, signed.String(), body); status != http.StatusForbidden {
		t.Errorf("a PUT past the URL's expiry answered %d, want %d", status, http.StatusForbidden)
	}
}

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
