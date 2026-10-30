package consolev1_test

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func validDispatchJobRequest() *consolev1.DispatchJobRequest {
	return &consolev1.DispatchJobRequest{
		RequestId: strings.Repeat("0a", 16),
		ProjectId: projectID,
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		RunnerId:  "ocel-cloud",
		Slug:      "shop",
		Job: &runnerv1.JobRequest{
			Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY,
			Source:  &runnerv1.JobRequest_SourceId{SourceId: strings.Repeat("ab", 32)},
		},
	}
}

func TestProtovalidateAcceptsEveryJobTheCLICanDispatch(t *testing.T) {
	requireAccepted(t, "a deploy on Ocel Cloud's runner", validDispatchJobRequest())

	bound := validDispatchJobRequest()
	bound.RunnerId, bound.Provider = "0199b5c4-0000-7000-8000-00000000cafe", "aws"
	requireAccepted(t, "a deploy on a bound runner", bound)

	preview := validDispatchJobRequest()
	preview.Tier = environmentv1.Tier_TIER_PREVIEW
	preview.Job.Command, preview.Job.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, &runnerv1.PreviewTarget{Name: "pr-12"}
	requireAccepted(t, "a preview rm", preview)

	requireAccepted(t, "a job read", &consolev1.GetJobRequest{ProjectId: projectID, JobId: "job_1"})
}

func TestProtovalidateRefusesAJobTheConsoleCannotDispatch(t *testing.T) {
	requireRefusals(t, validDispatchJobRequest, []refusal[*consolev1.DispatchJobRequest]{
		{name: "a request id is named", mutate: func(r *consolev1.DispatchJobRequest) { r.RequestId = "" }},
		{name: "a project is a uuid", mutate: func(r *consolev1.DispatchJobRequest) { r.ProjectId = "shop" }},
		{name: "a runner is a registered endpoint or Ocel Cloud's", mutate: func(r *consolev1.DispatchJobRequest) { r.RunnerId = "my-box" }},
		{name: "a deploy runs in production", mutate: func(r *consolev1.DispatchJobRequest) { r.Tier = environmentv1.Tier_TIER_PREVIEW }, rule: "dispatch_job.tier_of_command"},
		{name: "a client dispatches only an uploaded source", mutate: func(r *consolev1.DispatchJobRequest) {
			r.Job.Source = &runnerv1.JobRequest_GitSource{GitSource: &runnerv1.GitSource{CloneUrl: "https://github.com/example/shop.git", Commit: strings.Repeat("0a", 20), Token: "t"}}
		}, rule: "dispatch_job.uploaded_source"},
		{name: "a provider is a provider id", mutate: func(r *consolev1.DispatchJobRequest) { r.Provider = "AWS Cloud" }},
		{name: "a job is described", mutate: func(r *consolev1.DispatchJobRequest) { r.Job = nil }},
	})
}
