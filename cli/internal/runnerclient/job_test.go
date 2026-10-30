package runnerclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
)

type jobRead struct {
	job    *runnerv1.Job
	events []*runnerv1.JobEvent
	more   bool
	code   connect.Code
}

type fakeJobs struct {
	runnerv1connect.UnimplementedRunnerServiceHandler

	mu       sync.Mutex
	refused  error
	after    []uint64
	reads    []jobRead
	asked    []string
	canceled int
}

func serveJobs(t *testing.T, fake *fakeJobs) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(runnerv1connect.NewRunnerServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor())))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (f *fakeJobs) ListJobEvents(_ context.Context, req *runnerv1.ListJobEventsRequest) (*runnerv1.ListJobEventsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.GetJobId())
	f.after = append(f.after, req.GetAfterSeq())
	if f.refused != nil {
		return nil, f.refused
	}
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
	return &runnerv1.ListJobEventsResponse{Job: read.job, Events: read.events, More: read.more}, nil
}

func (f *fakeJobs) CancelJob(context.Context, *runnerv1.CancelJobRequest) (*runnerv1.CancelJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled++
	return &runnerv1.CancelJobResponse{}, nil
}

func job(state runnerv1.JobState, phase runnerv1.JobPhase, step string) jobRead {
	return jobRead{job: &runnerv1.Job{Id: "job_1", State: state, Phase: phase, Step: step}}
}

func failure(code connect.Code) jobRead {
	return jobRead{code: code}
}

func TestTailJobReportsEachChangeAndReturnsTheEndedJob(t *testing.T) {
	for _, ended := range []runnerv1.JobState{runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobState_JOB_STATE_FAILED, runnerv1.JobState_JOB_STATE_CANCELED} {
		t.Run(ended.String(), func(t *testing.T) {
			fake := &fakeJobs{reads: []jobRead{
				job(runnerv1.JobState_JOB_STATE_QUEUED, 0, ""),
				job(runnerv1.JobState_JOB_STATE_QUEUED, 0, ""),
				job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
				job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
				job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building api"),
				job(ended, runnerv1.JobPhase_JOB_PHASE_BUILD, ""),
			}}
			c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})

			var steps []string
			got, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(j *runnerv1.Job) {
				steps = append(steps, j.GetState().String()+" "+j.GetStep())
			}})
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
			waits := *slept
			if len(waits) != 5 {
				t.Fatalf("waited %d times, want once between each of the 6 reads", len(waits))
			}
			for _, i := range []int{0, 2, 4} {
				if waits[i] != pollEvery {
					t.Fatalf("waited %s after read %d showed a change, want %s", waits[i], i+1, pollEvery)
				}
			}
			for _, i := range []int{1, 3} {
				if waits[i] <= pollEvery {
					t.Fatalf("waited %s after read %d showed no change, want longer than %s", waits[i], i+1, pollEvery)
				}
			}
		})
	}
}

func TestTailJobPollsLessOftenWhileAJobShowsNoChangeUpToACap(t *testing.T) {
	reads := slices.Repeat([]jobRead{job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building web")}, 10)
	reads = append(reads, job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building api"), job(runnerv1.JobState_JOB_STATE_SUCCEEDED, 0, ""))
	c, slept := newTestClient(serveJobs(t, &fakeJobs{reads: reads}), &staticTokens{})

	if _, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}}); err != nil {
		t.Fatal(err)
	}
	waits := *slept
	if len(waits) != 11 || waits[0] != pollEvery || waits[10] != pollEvery {
		t.Fatalf("waited %v, want %s after each change", waits, pollEvery)
	}
	for i, d := range waits[1:10] {
		ceiling := min(pollEvery<<(i+1), maxPollBackoff)
		if d < ceiling/2 || d >= ceiling {
			t.Fatalf("unchanged read %d waited %s, want within [%s, %s)", i+1, d, ceiling/2, ceiling)
		}
	}
}

func TestTailJobKeepsWaitingWhileAJobNeedsInput(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(runnerv1.JobState_JOB_STATE_INPUT_REQUIRED, runnerv1.JobPhase_JOB_PHASE_CHECK, "Waiting for variables"),
		job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building web"),
		job(runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobPhase_JOB_PHASE_BUILD, ""),
	}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})
	got, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	if err != nil || got.GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("got %v, err = %v, want the wait to outlast the input", got, err)
	}
}

func TestTailJobReportsEachNewInputAJobWaitsFor(t *testing.T) {
	waitingFor := func(id string) jobRead {
		read := job(runnerv1.JobState_JOB_STATE_INPUT_REQUIRED, runnerv1.JobPhase_JOB_PHASE_BUILD, "")
		read.job.Input = &runnerv1.JobInput{Id: id, Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Question: "Continue?"}}}
		return read
	}
	fake := &fakeJobs{reads: []jobRead{
		waitingFor("in_1"),
		waitingFor("in_1"),
		waitingFor("in_2"),
		job(runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobPhase_JOB_PHASE_BUILD, ""),
	}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	var inputs []string
	if _, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(j *runnerv1.Job) { inputs = append(inputs, j.GetInput().GetId()) }}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(inputs, "|") != "in_1|in_2|" {
		t.Fatalf("reported inputs %q, want each input once and then the ended job", inputs)
	}
}

func TestTailJobRidesOutTransientFailuresWithBackoff(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(runnerv1.JobState_JOB_STATE_RUNNING, 0, ""),
		failure(connect.CodeUnavailable),
		failure(connect.CodeInternal),
		failure(connect.CodeDeadlineExceeded),
		job(runnerv1.JobState_JOB_STATE_SUCCEEDED, 0, ""),
	}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	got, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	if err != nil || got.GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED {
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

func TestTailJobGivesUpWhenOcelCloudStaysUnreachable(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(runnerv1.JobState_JOB_STATE_RUNNING, 0, "Building web"),
		failure(connect.CodeUnavailable),
	}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	last, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
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

func TestTailJobFailsAtOnceOnAnAnswerThatWillNotChange(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{failure(connect.CodePermissionDenied)}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})
	if _, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if len(*slept) != 0 || len(fake.asked) != 1 {
		t.Fatalf("asked %d times after %d waits, want one read", len(fake.asked), len(*slept))
	}
}

func TestTailJobStopsWatchingWhenInterruptedAndNeverCancelsTheJob(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "Building web")}}
	c := New("Ocel Cloud", serveJobs(t, fake).URL, &staticTokens{})
	ctx, cancel := context.WithCancel(t.Context())
	last, err := c.TailJob(ctx, "job_1", Tail{Changed: func(*runnerv1.Job) { cancel() }})
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

func TestTailJobLogsEachEventOnceInOrderAndReadsOnlyLaterOnes(t *testing.T) {
	event := func(seq uint64, message string) *runnerv1.JobEvent {
		return &runnerv1.JobEvent{Seq: seq, Message: message}
	}
	running := job(runnerv1.JobState_JOB_STATE_RUNNING, runnerv1.JobPhase_JOB_PHASE_BUILD, "")
	firstPage, secondPage := running, running
	firstPage.events, firstPage.more = []*runnerv1.JobEvent{event(1, "cloning"), event(2, "building")}, true
	secondPage.events = []*runnerv1.JobEvent{event(3, "built")}
	ended := job(runnerv1.JobState_JOB_STATE_SUCCEEDED, 0, "")
	ended.events = []*runnerv1.JobEvent{event(4, "deployed")}
	fake := &fakeJobs{reads: []jobRead{firstPage, secondPage, ended}}
	c, slept := newTestClient(serveJobs(t, fake), &staticTokens{})

	var logged []string
	if _, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}, Logged: func(e *runnerv1.JobEvent) { logged = append(logged, e.GetMessage()) }}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(logged, "|") != "cloning|building|built|deployed" {
		t.Fatalf("logged %q, want each event once, in order", logged)
	}
	if !slices.Equal(fake.after, []uint64{0, 2, 3}) {
		t.Fatalf("read after %v, want each read to start after the last event seen", fake.after)
	}
	if len(*slept) != 1 {
		t.Fatalf("waited %d times, want no wait before a page the runner said was more", len(*slept))
	}
}

func TestTailJobAsksWhatToDoWhileTheRunnerDoesNotKnowTheJobYet(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{failure(connect.CodeNotFound), failure(connect.CodeNotFound), job(runnerv1.JobState_JOB_STATE_SUCCEEDED, 0, "")}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})
	asked := 0
	got, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}, Unknown: func(context.Context) (*runnerv1.Job, error) {
		asked++
		return nil, nil
	}})
	if err != nil || got.GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED || asked != 2 {
		t.Fatalf("got %v, err = %v after asking %d times, want the job tailed once the runner knows it", got, err, asked)
	}

	fake = &fakeJobs{reads: []jobRead{failure(connect.CodeNotFound)}}
	c, _ = newTestClient(serveJobs(t, fake), &staticTokens{})
	failed := &runnerv1.Job{Id: "job_1", State: runnerv1.JobState_JOB_STATE_FAILED}
	got, err = c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}, Unknown: func(context.Context) (*runnerv1.Job, error) { return failed, nil }})
	if err != nil || got != failed {
		t.Fatalf("got %v, err = %v, want the tail to end with the job the console reports ended", got, err)
	}
}
