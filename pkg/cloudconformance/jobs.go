package cloudconformance

import (
	"fmt"
	"slices"
	"testing"

	"connectrpc.com/connect"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func runJobs(t *testing.T, c cloud) {
	t.Run("StartJob answers the job it was asked for, queued or running", func(t *testing.T) {
		source := c.cli.commit(t, c.cli.upload(t, "index.ts"))
		asked := buildRequest(source)

		started, err := c.cli.jobs.StartJob(t.Context(), asked)
		if err != nil {
			t.Fatalf("StartJob() of a committed source error = %v, want a job", err)
		}
		job := started.GetJob()
		c.console.cancelWhenTheTestEnds(t, job.GetId())
		if job.GetId() == "" || job.GetProjectId() != asked.GetProjectId() || job.GetCommand() != asked.GetCommand() || job.GetSourceId() != source {
			t.Errorf("StartJob() = %v, want an id and the project, command and source asked for", job)
		}
		if state := job.GetState(); state != cloudv1.JobState_JOB_STATE_QUEUED && state != cloudv1.JobState_JOB_STATE_RUNNING {
			t.Errorf("StartJob() answers the job %s, want it queued or running", state)
		}
	})

	t.Run("StartJob refuses a source the organization never committed", func(t *testing.T) {
		theirs := c.cli.commit(t, c.cli.upload(t, "index.ts"))

		for name, source := range map[string]string{
			"a source nobody committed":     digestOf(freshContent()),
			"another organization's source": theirs,
		} {
			started, err := c.otherCLI.jobs.StartJob(t.Context(), buildRequest(source))
			c.otherConsole.cancelWhenTheTestEnds(t, started.GetJob().GetId())
			if !refusedWith(err, connect.CodeNotFound) {
				t.Errorf("StartJob() of %s error = %v, want %s", name, err, connect.CodeNotFound)
			}
		}
	})

	t.Run("GetJob answers a job to its organization and finds no such job for another", func(t *testing.T) {
		job := c.start(t)

		read, err := c.console.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: job.GetId()})
		if err != nil || read.GetJob().GetId() != job.GetId() {
			t.Errorf("GetJob(%s) = %v, %v, want the job", job.GetId(), read.GetJob(), err)
		}
		_, err = c.otherConsole.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: job.GetId()})
		refusesAsNotFound(t, "GetJob() of another organization's job", err)
		_, err = c.console.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: "job_never_started"})
		refusesAsNotFound(t, "GetJob() of a job never started", err)
	})

	t.Run("ListJobEvents pages a job's events after a seq, at most limit at a time, saying whether more follow", func(t *testing.T) {
		pagesTheEvents(t, c)
	})

	t.Run("CancelJob ends a running job canceled", func(t *testing.T) {
		job := c.startRunning(t)

		canceled, err := c.console.jobs.CancelJob(t.Context(), &cloudv1.CancelJobRequest{JobId: job.GetId()})
		if err != nil || canceled.GetJob().GetState() != cloudv1.JobState_JOB_STATE_CANCELED || canceled.GetJob().GetEndedAt() == nil {
			t.Fatalf("CancelJob() of a running job = %v, %v, want the job canceled with the time it ended", canceled.GetJob(), err)
		}
		c.await(t, job.GetId(), cloudv1.JobState_JOB_STATE_CANCELED)
	})

	t.Run("CancelJob answers an ended job as it ended", func(t *testing.T) {
		job := c.startSucceeded(t)

		canceled, err := c.console.jobs.CancelJob(t.Context(), &cloudv1.CancelJobRequest{JobId: job.GetId()})
		if err != nil || canceled.GetJob().GetState() != cloudv1.JobState_JOB_STATE_SUCCEEDED {
			t.Errorf("CancelJob() of a succeeded job = %v, %v, want the job answered succeeded and no error", canceled.GetJob(), err)
		}
		c.await(t, job.GetId(), cloudv1.JobState_JOB_STATE_SUCCEEDED)
	})

	t.Run("CancelJob finds no such job for another organization and leaves it running", func(t *testing.T) {
		job := c.startRunning(t)

		_, err := c.otherConsole.jobs.CancelJob(t.Context(), &cloudv1.CancelJobRequest{JobId: job.GetId()})
		refusesAsNotFound(t, "CancelJob() of another organization's job", err)
		c.await(t, job.GetId(), cloudv1.JobState_JOB_STATE_RUNNING)
	})

	t.Run("RespondJob refuses a job that waits on no input", func(t *testing.T) {
		ended := c.startSucceeded(t)
		running := c.startRunning(t)

		for name, id := range map[string]string{"running": running.GetId(), "succeeded": ended.GetId()} {
			_, err := c.console.jobs.RespondJob(t.Context(), confirm(id))
			if !refusedWith(err, connect.CodeFailedPrecondition) {
				t.Errorf("RespondJob() of a %s job error = %v, want %s", name, err, connect.CodeFailedPrecondition)
			}
		}
		_, err := c.otherConsole.jobs.RespondJob(t.Context(), confirm(running.GetId()))
		refusesAsNotFound(t, "RespondJob() of another organization's job", err)
	})
}

func confirm(id string) *cloudv1.RespondJobRequest {
	return &cloudv1.RespondJobRequest{JobId: id, InputId: "input_1", Answer: &cloudv1.RespondJobRequest_Confirmed{Confirmed: true}}
}

func pagesTheEvents(t *testing.T, c cloud) {
	t.Helper()

	job := c.startRunning(t)
	lines := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	if err := c.suite.Builder.Log(t.Context(), job.GetId(), lines); err != nil {
		t.Fatalf("Builder.Log(%s) error = %v", job.GetId(), err)
	}

	var messages []string
	var shapes []string
	after := uint64(0)
	for range len(lines) {
		page, err := c.console.jobs.ListJobEvents(t.Context(), &cloudv1.ListJobEventsRequest{JobId: job.GetId(), AfterSeq: after, Limit: 2})
		if err != nil {
			t.Fatalf("ListJobEvents(after %d, limit 2) error = %v", after, err)
		}
		if page.GetJob().GetId() != job.GetId() {
			t.Errorf("ListJobEvents() answers job %v, want the job its events are of", page.GetJob())
		}
		shapes = append(shapes, fmt.Sprintf("%d more=%t", len(page.GetEvents()), page.GetMore()))
		for _, event := range page.GetEvents() {
			if event.GetSeq() <= after {
				t.Errorf("ListJobEvents(after %d) answers seq %d, want only later events, in order", after, event.GetSeq())
			}
			after = event.GetSeq()
			messages = append(messages, event.GetMessage())
		}
		if !page.GetMore() {
			break
		}
	}
	if want := []string{"2 more=true", "2 more=true", "1 more=false"}; !slices.Equal(shapes, want) {
		t.Errorf("ListJobEvents() pages of limit 2 over five events = %v, want %v; Builder.Log returns once its lines are the job's events", shapes, want)
	}
	if !slices.Equal(messages, lines) {
		t.Errorf("ListJobEvents() pages carry %q, want the lines logged %q, in order", messages, lines)
	}

	past, err := c.console.jobs.ListJobEvents(t.Context(), &cloudv1.ListJobEventsRequest{JobId: job.GetId(), AfterSeq: after})
	if err != nil || len(past.GetEvents()) != 0 || past.GetMore() {
		t.Errorf("ListJobEvents(after the last seq) = %v, %v, want no events and no more", past, err)
	}
	all, err := c.console.jobs.ListJobEvents(t.Context(), &cloudv1.ListJobEventsRequest{JobId: job.GetId()})
	if err != nil || len(all.GetEvents()) != len(lines) || all.GetMore() {
		t.Errorf("ListJobEvents() with no limit = %v, %v, want all %d events and no more", all, err, len(lines))
	}
	_, err = c.otherConsole.jobs.ListJobEvents(t.Context(), &cloudv1.ListJobEventsRequest{JobId: job.GetId()})
	refusesAsNotFound(t, "ListJobEvents() of another organization's job", err)
}
