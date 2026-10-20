package cloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/proto"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
)

type jobRead struct {
	job  *cloudv1.Job
	code connect.Code
}

type fakeJobs struct {
	cloudv1connect.UnimplementedJobServiceHandler

	mu       sync.Mutex
	started  []*cloudv1.StartJobRequest
	refuse   []connect.Code
	reads    []jobRead
	asked    []string
	canceled int
}

func serveJobs(t *testing.T, fake *fakeJobs) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(cloudv1connect.NewJobServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor())))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (f *fakeJobs) StartJob(_ context.Context, req *cloudv1.StartJobRequest) (*cloudv1.StartJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, req)
	if len(f.refuse) > 0 {
		code := f.refuse[0]
		f.refuse = f.refuse[1:]
		return nil, connect.NewError(code, errors.New("refused"))
	}
	return &cloudv1.StartJobResponse{Job: &cloudv1.Job{Id: "job_1", ProjectId: req.GetProjectId(), Command: req.GetCommand(), State: cloudv1.JobState_JOB_STATE_QUEUED, SourceId: req.GetSourceId()}}, nil
}

func (f *fakeJobs) GetJob(_ context.Context, req *cloudv1.GetJobRequest) (*cloudv1.GetJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.GetJobId())
	if len(f.reads) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the script ran out"))
	}
	read := f.reads[0]
	if len(f.reads) > 1 {
		f.reads = f.reads[1:]
	}
	if read.code != 0 {
		return nil, connect.NewError(read.code, errors.New("unreachable"))
	}
	return &cloudv1.GetJobResponse{Job: read.job}, nil
}

func (f *fakeJobs) CancelJob(context.Context, *cloudv1.CancelJobRequest) (*cloudv1.CancelJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled++
	return &cloudv1.CancelJobResponse{}, nil
}

func job(state cloudv1.JobState, phase cloudv1.JobPhase, step string) jobRead {
	return jobRead{job: &cloudv1.Job{Id: "job_1", State: state, Phase: phase, Step: step}}
}

func failure(code connect.Code) jobRead {
	return jobRead{code: code}
}

func startBuild() *cloudv1.StartJobRequest {
	return &cloudv1.StartJobRequest{
		ProjectId:  "proj_1",
		Slug:       "shop",
		Command:    cloudv1.JobCommand_JOB_COMMAND_BUILD,
		SourceId:   strings.Repeat("ab", 32),
		CliVersion: "dev",
	}
}

func TestStartJobSendsTheTypedRequestWithTheOcelCloudToken(t *testing.T) {
	fake := &fakeJobs{}
	server := serveJobs(t, fake)
	c, _ := newTestClient(server, &staticTokens{})

	started, err := c.StartJob(t.Context(), startBuild())
	if err != nil {
		t.Fatal(err)
	}
	if started.GetId() != "job_1" || started.GetState() != cloudv1.JobState_JOB_STATE_QUEUED {
		t.Fatalf("started %v, want job_1 queued", started)
	}
	if len(fake.started) != 1 || !proto.Equal(fake.started[0], startBuild()) {
		t.Fatalf("Ocel Cloud saw %v, want the request as sent", fake.started)
	}
}

func TestStartJobTriesAgainOnlyWhenOcelCloudAsksToSlowDown(t *testing.T) {
	t.Run("throttled", func(t *testing.T) {
		fake := &fakeJobs{refuse: []connect.Code{connect.CodeResourceExhausted}}
		c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
		if _, err := c.StartJob(t.Context(), startBuild()); err != nil {
			t.Fatal(err)
		}
		if len(fake.started) != 2 || len(*slept) != 1 {
			t.Fatalf("started %d times after %d waits, want a second start after one wait", len(fake.started), len(*slept))
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		fake := &fakeJobs{refuse: []connect.Code{connect.CodeUnavailable}}
		c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})
		if _, err := c.StartJob(t.Context(), startBuild()); connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("err = %v, want the failure as Ocel Cloud answered it", err)
		}
		if len(fake.started) != 1 {
			t.Fatalf("started %d times, want once: the first may have started a job", len(fake.started))
		}
	})
}

func TestWaitForJobReportsEachChangeAndReturnsTheEndedJob(t *testing.T) {
	for _, ended := range []cloudv1.JobState{cloudv1.JobState_JOB_STATE_SUCCEEDED, cloudv1.JobState_JOB_STATE_FAILED, cloudv1.JobState_JOB_STATE_CANCELED} {
		t.Run(ended.String(), func(t *testing.T) {
			fake := &fakeJobs{reads: []jobRead{
				job(cloudv1.JobState_JOB_STATE_QUEUED, 0, ""),
				job(cloudv1.JobState_JOB_STATE_QUEUED, 0, ""),
				job(cloudv1.JobState_JOB_STATE_RUNNING, cloudv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
				job(cloudv1.JobState_JOB_STATE_RUNNING, cloudv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
				job(cloudv1.JobState_JOB_STATE_RUNNING, cloudv1.JobPhase_JOB_PHASE_BUILD, "Building api"),
				job(ended, cloudv1.JobPhase_JOB_PHASE_BUILD, ""),
			}}
			c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})

			var steps []string
			got, err := c.WaitForJob(t.Context(), "job_1", func(j *cloudv1.Job) {
				steps = append(steps, j.GetState().String()+" "+j.GetStep())
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.GetState() != ended {
				t.Fatalf("ended %v, want %v", got.GetState(), ended)
			}
			want := []string{"JOB_STATE_QUEUED ", "JOB_STATE_RUNNING Building web", "JOB_STATE_RUNNING Building api", ended.String() + " "}
			if strings.Join(steps, "|") != strings.Join(want, "|") {
				t.Fatalf("reported %q, want %q", steps, want)
			}
			if len(*slept) != 5 {
				t.Fatalf("waited %d times, want once between each of the 6 reads", len(*slept))
			}
			for _, d := range *slept {
				if d != pollEvery {
					t.Fatalf("waited %s between reads, want %s", d, pollEvery)
				}
			}
		})
	}
}

func TestWaitForJobKeepsWaitingWhileAJobNeedsInput(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(cloudv1.JobState_JOB_STATE_INPUT_REQUIRED, cloudv1.JobPhase_JOB_PHASE_CHECK, "Waiting for variables"),
		job(cloudv1.JobState_JOB_STATE_RUNNING, cloudv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
		job(cloudv1.JobState_JOB_STATE_SUCCEEDED, cloudv1.JobPhase_JOB_PHASE_BUILD, ""),
	}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})
	got, err := c.WaitForJob(t.Context(), "job_1", func(*cloudv1.Job) {})
	if err != nil || got.GetState() != cloudv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("got %v, err = %v, want the wait to outlast the input", got, err)
	}
}

func TestWaitForJobReportsEachNewInputAJobWaitsFor(t *testing.T) {
	waitingFor := func(id string) jobRead {
		read := job(cloudv1.JobState_JOB_STATE_INPUT_REQUIRED, cloudv1.JobPhase_JOB_PHASE_BUILD, "")
		read.job.Input = &cloudv1.JobInput{Id: id, Need: &cloudv1.JobInput_Confirmation{Confirmation: &cloudv1.Confirmation{Question: "Continue?"}}}
		return read
	}
	fake := &fakeJobs{reads: []jobRead{
		waitingFor("in_1"),
		waitingFor("in_1"),
		waitingFor("in_2"),
		job(cloudv1.JobState_JOB_STATE_SUCCEEDED, cloudv1.JobPhase_JOB_PHASE_BUILD, ""),
	}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	var inputs []string
	if _, err := c.WaitForJob(t.Context(), "job_1", func(j *cloudv1.Job) { inputs = append(inputs, j.GetInput().GetId()) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(inputs, "|") != "in_1|in_2|" {
		t.Fatalf("reported inputs %q, want each input once and then the ended job", inputs)
	}
}

func TestWaitForJobRidesOutTransientFailuresWithBackoff(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(cloudv1.JobState_JOB_STATE_RUNNING, 0, ""),
		failure(connect.CodeUnavailable),
		failure(connect.CodeInternal),
		failure(connect.CodeDeadlineExceeded),
		job(cloudv1.JobState_JOB_STATE_SUCCEEDED, 0, ""),
	}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	got, err := c.WaitForJob(t.Context(), "job_1", func(*cloudv1.Job) {})
	if err != nil || got.GetState() != cloudv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("got %v, err = %v, want the job read once Ocel Cloud answered again", got, err)
	}
	waits := *slept
	if len(waits) != 4 || waits[0] != pollEvery {
		t.Fatalf("waited %v, want one poll and three backoffs", waits)
	}
	for i, d := range waits[1:] {
		ceiling := min(pollEvery<<(i+1), maxPollBackoff)
		if d < ceiling/2 || d >= ceiling {
			t.Fatalf("backoff %d waited %s, want within [%s, %s)", i+1, d, ceiling/2, ceiling)
		}
	}
}

func TestWaitForJobGivesUpWhenOcelCloudStaysUnreachable(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(cloudv1.JobState_JOB_STATE_RUNNING, 0, "Building web"),
		failure(connect.CodeUnavailable),
	}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	last, err := c.WaitForJob(t.Context(), "job_1", func(*cloudv1.Job) {})
	if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "keeps running") {
		t.Fatalf("err = %v, want Ocel Cloud named unreachable and the job still running", err)
	}
	if last.GetStep() != "Building web" {
		t.Fatalf("last = %v, want the last job read", last)
	}
	var total time.Duration
	for _, d := range (*slept)[1:] {
		total += d
	}
	if total > maxUnreachableWait || total < maxUnreachableWait-maxPollBackoff {
		t.Fatalf("waited %s on an unreachable Ocel Cloud, want close to %s", total, maxUnreachableWait)
	}
}

func TestWaitForJobFailsAtOnceOnAnAnswerThatWillNotChange(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{failure(connect.CodePermissionDenied)}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	if _, err := c.WaitForJob(t.Context(), "job_1", func(*cloudv1.Job) {}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if len(*slept) != 0 || len(fake.asked) != 1 {
		t.Fatalf("asked %d times after %d waits, want one read", len(fake.asked), len(*slept))
	}
}

func TestWaitForJobStopsWatchingWhenInterruptedAndNeverCancelsTheJob(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{job(cloudv1.JobState_JOB_STATE_RUNNING, cloudv1.JobPhase_JOB_PHASE_BUILD, "Building web")}}
	c := New(serveJobs(t, fake).URL, &staticTokens{})
	ctx, cancel := context.WithCancel(t.Context())
	last, err := c.WaitForJob(ctx, "job_1", func(*cloudv1.Job) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the wait interrupted", err)
	}
	if last.GetStep() != "Building web" {
		t.Fatalf("last = %v, want the last job read", last)
	}
	if fake.canceled != 0 {
		t.Fatal("cancelled the job")
	}
}
