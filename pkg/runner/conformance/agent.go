package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func runAgent(t *testing.T, r runnerUnderTest) {
	t.Run("GetSpec answers the job's identity, its request, and each blob of its source signed for a GET", func(t *testing.T) {
		file, body := r.mine.uploadContent(t, "src/index.ts")
		source := r.mine.commit(t, file)
		id := newJobID()
		agent := newCaller(r.suite.URL, r.jobToken(t, r.suite.Organization, id))
		asked := buildRequest(source)
		asked.Job.ProjectPath = "src"
		if _, err := agent.runner.StartJob(t.Context(), asked); err != nil {
			t.Fatalf("StartJob() error = %v", err)
		}
		r.mine.cancelWhenTheTestEnds(t, id)

		spec, err := agent.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
		if err != nil {
			t.Fatalf("GetSpec() error = %v", err)
		}
		if err := protovalidate.Validate(spec); err != nil {
			t.Errorf("GetSpec() answered job %s, which breaks the spec's rules: %v", spec.GetJobId(), err)
		}
		claims := r.jobTokenClaims(id)
		if spec.GetJobId() != id || spec.GetProjectId() != claims.ProjectID || spec.GetTier() != environmentv1.Tier_TIER_PRODUCTION ||
			spec.GetDeploymentId() != claims.DeploymentID || spec.GetSlug() != claims.Slug || spec.GetConsoleUrl() == "" {
			t.Errorf("GetSpec() = job %s, project %s, tier %s, deployment %s, slug %s, console %q; want the identity the job token holds and the console that minted it",
				spec.GetJobId(), spec.GetProjectId(), spec.GetTier(), spec.GetDeploymentId(), spec.GetSlug(), spec.GetConsoleUrl())
		}
		if spec.GetJob().GetSourceId() != source || spec.GetJob().GetProjectPath() != "src" {
			t.Errorf("GetSpec() runs source %s in %q, want the request StartJob was given", spec.GetJob().GetSourceId(), spec.GetJob().GetProjectPath())
		}
		blobs := spec.GetArchive().GetBlobs()
		if len(blobs) != 1 || blobs[0].GetDigest() != file.GetDigest() {
			t.Fatalf("GetSpec() archive blobs = %v, want the one blob of the source", blobs)
		}
		got := fetch(t, blobs[0].GetUrl())
		if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != file.GetDigest() || string(got) != body {
			t.Errorf("the blob's signed URL answered %q, want the bytes uploaded", got)
		}
		r.mine.await(t, id, runnerv1.JobState_JOB_STATE_RUNNING)
	})

	t.Run("an agent's calls refuse a job token for another runner and a person's token", func(t *testing.T) {
		job := r.start(t)
		claims := r.jobTokenClaims(job.id)
		claims.Runner = r.suite.Runner + "-other"
		elsewhere := newCaller(r.suite.URL, r.suite.JobToken(t, r.suite.Organization, claims))

		_, err := elsewhere.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
		refusesAsDenied(t, "GetSpec() bearing another runner's job token", err)
		_, err = r.mine.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
		refusesAsDenied(t, "GetSpec() bearing a person's token", err)
	})

	t.Run("a job of one organization is no job of another's, whatever id its token names", func(t *testing.T) {
		job := r.start(t)

		theirs := newCaller(r.suite.URL, r.jobToken(t, r.suite.OtherOrganization, job.id))
		_, err := theirs.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
		refusesAsNotFound(t, "GetSpec() of the same job id under another organization", err)
	})

	t.Run("an input the run waits on is answered, handed to the agent, and the job runs on to success", func(t *testing.T) {
		job := r.startRunning(t)

		waiting := `{"waiting":{"input":{"id":"in_1","confirmation":{"question":"Continue?"}}}}`
		job.appendOutput(t, 0, waiting)
		asked := r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_INPUT_REQUIRED)
		if asked.GetInput().GetId() != "in_1" || asked.GetInput().GetConfirmation().GetQuestion() != "Continue?" {
			t.Fatalf("the job waits on %v, want the run's input", asked.GetInput())
		}
		_, err := r.mine.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_0"))
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("AnswerConfirmation() of an input the job does not wait on error = %v, want %s", err, connect.CodeFailedPrecondition)
		}
		answered, err := r.mine.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_1"))
		if err != nil || answered.GetJob().GetState() != runnerv1.JobState_JOB_STATE_RUNNING {
			t.Fatalf("AnswerConfirmation() = %v, %v, want the job running on", answered.GetJob(), err)
		}
		line, err := job.agent.agent.AwaitAnswer(t.Context(), &runnerv1.AwaitAnswerRequest{})
		if err != nil {
			t.Fatalf("AwaitAnswer() error = %v", err)
		}
		answer := &streamv1.InputAnswer{}
		if err := protojson.Unmarshal([]byte(line.GetLine()), answer); err != nil || answer.GetInputId() != "in_1" || !answer.GetConfirmed() {
			t.Fatalf("AwaitAnswer() = %q, want the confirmation as one InputAnswer line", line.GetLine())
		}

		job.appendOutput(t, 1, `{"resumed":{"reason":"answered"}}`, `{"summary":{"success":true,"apps":[{"urls":["https://conformance.example.com"]}]}}`)
		if _, err := job.agent.agent.Finish(t.Context(), &runnerv1.FinishRequest{Builds: []*runnerv1.AppBuild{{App: "web", BuildId: strings.Repeat("a", 32)}}}); err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		done := r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_SUCCEEDED)
		if !slices.Equal(done.GetResult().GetUrls(), []string{"https://conformance.example.com"}) || done.GetEndedAt() == nil {
			t.Errorf("the job ended %v, want the run's URLs and the time it ended", done)
		}
		_, err = r.mine.runner.AnswerConfirmation(t.Context(), confirm(job.id, "in_1"))
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("AnswerConfirmation() of an ended job error = %v, want %s", err, connect.CodeFailedPrecondition)
		}
	})

	t.Run("a run that fails ends the job failed with its error", func(t *testing.T) {
		job := r.startRunning(t)

		job.appendOutput(t, 0, `{"summary":{"success":false,"headline":"Deploy failed","error":{"code":"project.invalid_config","message":"no app"}}}`)
		if _, err := job.agent.agent.Finish(t.Context(), &runnerv1.FinishRequest{ExitCode: 1}); err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		failed := r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_FAILED)
		if failed.GetError().GetCode() != "project.invalid_config" || failed.GetError().GetMessage() != "no app" {
			t.Errorf("the failed job's error = %v, want the run's", failed.GetError())
		}
	})

	t.Run("AppendOutput tells the agent to stop once the job is canceled", func(t *testing.T) {
		job := r.startRunning(t)

		if _, err := r.mine.runner.CancelJob(t.Context(), &runnerv1.CancelJobRequest{JobId: job.id}); err != nil {
			t.Fatalf("CancelJob() error = %v", err)
		}
		if appended := job.appendOutput(t, 0, "still running"); !appended.GetCancel() {
			t.Errorf("AppendOutput() after a cancel = %v, want it to tell the agent to stop", appended)
		}
	})

	t.Run("AppendOutput sent again from an earlier offset takes each line once", func(t *testing.T) {
		job := r.startRunning(t)

		job.appendOutput(t, 0, "one", "two")
		job.appendOutput(t, 1, "two", "three")
		page, err := r.mine.runner.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: job.id})
		if err != nil {
			t.Fatal(err)
		}
		var said []string
		for _, event := range page.GetEvents() {
			said = append(said, event.GetMessage())
		}
		if want := []string{"one", "two", "three"}; !slices.Equal(said, want) {
			t.Errorf("the job's events after a resent AppendOutput = %q, want %q", said, want)
		}
	})
}
