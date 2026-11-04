package conformance

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"

	"connectrpc.com/connect"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func runJobs(t *testing.T, r runnerUnderTest) {
	t.Run("StartJob answers the job its token names, queued or running", func(t *testing.T) {
		job := r.start(t)

		got := job.job
		if got.GetId() != job.id || got.GetProjectId() != conformanceProject || got.GetCommand() != runnerv1.JobCommand_JOB_COMMAND_DEPLOY {
			t.Errorf("StartJob() = %v, want job %s of the token's project, running the command asked for", got, job.id)
		}
		if state := got.GetState(); state != runnerv1.JobState_JOB_STATE_QUEUED && state != runnerv1.JobState_JOB_STATE_RUNNING {
			t.Errorf("StartJob() answers the job %s, want it queued or running", state)
		}
	})

	t.Run("StartJob again for a job it holds starts no second job, whatever token it bears", func(t *testing.T) {
		job := r.start(t)

		again := newCaller(r.suite.URL, r.jobToken(t, r.suite.Organization, job.id))
		started, err := again.runner.StartJob(t.Context(), buildRequest(job.source))
		if err != nil || started.GetJob().GetId() != job.id || !started.GetJob().GetCreatedAt().AsTime().Equal(job.job.GetCreatedAt().AsTime()) {
			t.Errorf("StartJob() again for job %s = %v, %v, want the job already held", job.id, started.GetJob(), err)
		}
	})

	t.Run("StartJob refuses a job token minted for another runner", func(t *testing.T) {
		source := r.mine.commit(t, r.mine.upload(t, "index.ts"))
		id := newJobID()
		claims := r.jobTokenClaims(id)
		claims.Runner = r.suite.Runner + "-other"
		elsewhere := newCaller(r.suite.URL, r.suite.JobToken(t, r.suite.Organization, claims))

		_, err := elsewhere.runner.StartJob(t.Context(), buildRequest(source))
		refusesAsDenied(t, "StartJob() bearing another runner's job token", err)
		_, err = r.mine.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: id})
		refusesAsNotFound(t, "GetJob() of a job refused at its start", err)
	})

	t.Run("StartJob holds a job to the preview and tier its token names", func(t *testing.T) {
		source := r.mine.commit(t, r.mine.upload(t, "index.ts"))
		previewUp := func(name string) *runnerv1.StartJobRequest {
			return &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{
				Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Source: &runnerv1.JobRequest_SourceId{SourceId: source},
				Preview: &runnerv1.PreviewTarget{Name: name},
			}}
		}
		deploy := buildRequest(source)
		claims := func(preview string, tier environment.Tier) endpoint.Job {
			job := r.jobTokenClaims(newJobID())
			job.Preview, job.Tier = preview, tier
			return job
		}
		for _, tt := range []struct {
			name    string
			claims  endpoint.Job
			request *runnerv1.StartJobRequest
			denied  bool
		}{
			{"a preview up of the preview its token names", claims("feat-x", environment.TierPreview), previewUp("feat-x"), false},
			{"a preview up of another preview than its token names", claims("feat-x", environment.TierPreview), previewUp("feat-y"), true},
			{"a preview up whose token names no preview", claims("", environment.TierPreview), previewUp("feat-x"), true},
			{"a preview up on a production token", claims("feat-x", environment.TierProduction), previewUp("feat-x"), true},
			{"a deploy whose token names a preview", claims("feat-x", environment.TierProduction), deploy, true},
			{"a deploy on a preview-tier token", claims("", environment.TierPreview), deploy, true},
		} {
			agent := newCaller(r.suite.URL, r.suite.JobToken(t, r.suite.Organization, tt.claims))
			started, err := agent.runner.StartJob(t.Context(), tt.request)
			if tt.denied {
				refusesAsDenied(t, "StartJob() of "+tt.name, err)
				continue
			}
			if err != nil {
				t.Errorf("StartJob() of %s error = %v, want the job started", tt.name, err)
				continue
			}
			r.mine.cancelWhenTheTestEnds(t, started.GetJob().GetId())
		}
	})

	t.Run("StartJob refuses a person's token: only the console starts a job", func(t *testing.T) {
		source := r.mine.commit(t, r.mine.upload(t, "index.ts"))

		_, err := r.mine.runner.StartJob(t.Context(), buildRequest(source))
		refusesAsDenied(t, "StartJob() bearing a person's token", err)
	})

	t.Run("StartJob refuses a source the organization never committed", func(t *testing.T) {
		theirs := r.mine.commit(t, r.mine.upload(t, "index.ts"))

		for name, source := range map[string]string{
			"a source nobody committed":     digestOf(freshContent()),
			"another organization's source": theirs,
		} {
			agent := newCaller(r.suite.URL, r.jobToken(t, r.suite.OtherOrganization, newJobID()))
			started, err := agent.runner.StartJob(t.Context(), buildRequest(source))
			if started != nil {
				r.other.cancelWhenTheTestEnds(t, started.GetJob().GetId())
			}
			if !refusedWith(err, connect.CodeNotFound) {
				t.Errorf("StartJob() of %s error = %v, want %s", name, err, connect.CodeNotFound)
			}
		}
	})

	t.Run("GetJob answers a job to its organization and finds no such job for another", func(t *testing.T) {
		job := r.start(t)

		read, err := r.mine.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: job.id})
		if err != nil || read.GetJob().GetId() != job.id {
			t.Errorf("GetJob(%s) = %v, %v, want the job", job.id, read.GetJob(), err)
		}
		_, err = r.other.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: job.id})
		refusesAsNotFound(t, "GetJob() of another organization's job", err)
		_, err = r.mine.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: "job_never_started"})
		refusesAsNotFound(t, "GetJob() of a job never started", err)
	})

	t.Run("ListJobEvents pages a job's events after a seq, at most limit at a time, saying whether more follow", func(t *testing.T) {
		pagesTheEvents(t, r)
	})

	t.Run("CancelJob ends a running job canceled", func(t *testing.T) {
		job := r.startRunning(t)

		canceled, err := r.mine.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id})
		if err != nil {
			t.Fatalf("CancelJob() of a running job error = %v", err)
		}
		if state := canceled.GetJob().GetState(); state == runnerv1.JobState_JOB_STATE_CANCELED && canceled.GetJob().GetEndedAt() == nil {
			t.Errorf("CancelJob() answered %v, canceled with no time it ended", canceled.GetJob())
		}
		r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_CANCELED)
	})

	t.Run("CancelJob answers an ended job as it ended", func(t *testing.T) {
		job := r.startSucceeded(t)

		canceled, err := r.mine.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id})
		if err != nil || canceled.GetJob().GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED {
			t.Errorf("CancelJob() of a succeeded job = %v, %v, want the job answered succeeded and no error", canceled.GetJob(), err)
		}
	})

	t.Run("CancelJob finds no such job for another organization and leaves it running", func(t *testing.T) {
		job := r.startRunning(t)

		_, err := r.other.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id})
		refusesAsNotFound(t, "CancelJob() of another organization's job", err)
		r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_RUNNING)
	})

	t.Run("AnswerConfirmation and SupplyVariables refuse a job that waits on no input, and find none of another organization's", func(t *testing.T) {
		ended := r.startSucceeded(t)
		running := r.startRunning(t)

		for name, id := range map[string]string{"running": running.id, "succeeded": ended.id} {
			_, err := r.mine.runner.AnswerConfirmation(t.Context(), confirm(id, "input_1"))
			if !refusedWith(err, connect.CodeFailedPrecondition) {
				t.Errorf("AnswerConfirmation() of a %s job error = %v, want %s", name, err, connect.CodeFailedPrecondition)
			}
			_, err = r.mine.runner.SupplyVariables(t.Context(), supply(id, "input_1"))
			if !refusedWith(err, connect.CodeFailedPrecondition) {
				t.Errorf("SupplyVariables() of a %s job error = %v, want %s", name, err, connect.CodeFailedPrecondition)
			}
		}
		_, err := r.other.runner.AnswerConfirmation(t.Context(), confirm(running.id, "input_1"))
		refusesAsNotFound(t, "AnswerConfirmation() of another organization's job", err)
		_, err = r.other.runner.SupplyVariables(t.Context(), supply(running.id, "input_1"))
		refusesAsNotFound(t, "SupplyVariables() of another organization's job", err)
	})

	t.Run("a member neither answers nor cancels a destroy job, which an admin does", func(t *testing.T) {
		job := r.waitingOnConfirmation(t, runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY)

		_, err := r.member.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_1"))
		refusesAsDenied(t, "AnswerConfirmation() of a destroy job by a member", err)
		_, err = r.member.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id})
		refusesAsDenied(t, "CancelJob() of a destroy job by a member", err)
		r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_INPUT_REQUIRED)

		answered, err := r.admin.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_1"))
		if err != nil || answered.GetJob().GetState() != runnerv1.JobState_JOB_STATE_RUNNING {
			t.Fatalf("AnswerConfirmation() of a destroy job by an admin = %v, %v, want the job running on", answered.GetJob(), err)
		}
		if _, err := r.admin.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
			t.Fatalf("CancelJob() of a destroy job by an admin error = %v", err)
		}
		r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_CANCELED)
	})

	t.Run("a member answers and cancels a job whose command a member may run", func(t *testing.T) {
		job := r.waitingOnConfirmation(t, runnerv1.JobCommand_JOB_COMMAND_DEPLOY)

		if _, err := r.member.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_1")); err != nil {
			t.Fatalf("AnswerConfirmation() of a deploy job by a member error = %v", err)
		}
		if _, err := r.member.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
			t.Fatalf("CancelJob() of a deploy job by a member error = %v", err)
		}
		r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_CANCELED)
	})
}

func (r runnerUnderTest) waitingOnConfirmation(t *testing.T, command runnerv1.JobCommand) startedJob {
	t.Helper()

	job := r.startRunningCommand(t, command)
	job.appendOutput(t, 0, `{"waiting":{"input":{"id":"in_1","confirmation":{"question":"Continue?"}}}}`)
	r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_INPUT_REQUIRED)
	return job
}

func confirm(id, input string) *runnerv1.AnswerConfirmationRequest {
	return &runnerv1.AnswerConfirmationRequest{JobId: id, InputId: input, Confirmed: true}
}

func supply(id, input string) *runnerv1.SupplyVariablesRequest {
	return &runnerv1.SupplyVariablesRequest{JobId: id, InputId: input, Variables: &runnerv1.VariableValues{
		Values: []*runnerv1.VariableValue{{Key: "GREETING", Value: "hello"}},
	}}
}

func pagesTheEvents(t *testing.T, r runnerUnderTest) {
	t.Helper()

	job := r.startRunning(t)
	lines := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	job.appendOutput(t, 0, lines...)

	var messages []string
	var shapes []string
	after := uint64(0)
	for range len(lines) {
		page, err := r.mine.runner.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id, AfterSeq: after, Limit: 2})
		if err != nil {
			t.Fatalf("ListJobEvents(after %d, limit 2) error = %v", after, err)
		}
		if page.GetJob().GetId() != job.id {
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
		t.Errorf("ListJobEvents() pages of limit 2 over five events = %v, want %v; AppendOutput returns once its lines are the job's events", shapes, want)
	}
	if !slices.Equal(messages, lines) {
		t.Errorf("ListJobEvents() pages carry %q, want the lines appended %q, in order", messages, lines)
	}

	past, err := r.mine.runner.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id, AfterSeq: after})
	if err != nil || len(past.GetEvents()) != 0 || past.GetMore() {
		t.Errorf("ListJobEvents(after the last seq) = %v, %v, want no events and no more", past, err)
	}
	all, err := r.mine.runner.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id})
	if err != nil || len(all.GetEvents()) != len(lines) || all.GetMore() {
		t.Errorf("ListJobEvents() with no limit = %v, %v, want all %d events and no more", all, err, len(lines))
	}
	_, err = r.other.runner.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id})
	refusesAsNotFound(t, "ListJobEvents() of another organization's job", err)
}
