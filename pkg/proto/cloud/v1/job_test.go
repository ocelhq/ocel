package cloudv1_test

import (
	"strings"
	"testing"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const projectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

func validStartBuild() *cloudv1.StartJobRequest {
	return &cloudv1.StartJobRequest{
		ProjectId:  projectID,
		Slug:       "shop",
		Command:    cloudv1.JobCommand_JOB_COMMAND_BUILD,
		SourceId:   digest,
		CliVersion: "0.4.1",
		Git:        &cloudv1.GitContext{Commit: "0a1b2c3d4e5f", Branch: "feat/cart", Dirty: true, PullRequest: 12},
	}
}

func TestProtovalidateAcceptsEveryJobRequestTheCLICanSend(t *testing.T) {
	requireAccepted(t, "a build", validStartBuild())
	requireAccepted(t, "a build outside git", &cloudv1.StartJobRequest{ProjectId: projectID, Slug: "shop", Command: cloudv1.JobCommand_JOB_COMMAND_BUILD, SourceId: digest, CliVersion: "dev"})

	deploy := validStartBuild()
	deploy.Command, deploy.Tag = cloudv1.JobCommand_JOB_COMMAND_DEPLOY, "v1.2.0"
	requireAccepted(t, "a tagged deploy", deploy)

	preview := validStartBuild()
	preview.Command, preview.Preview = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &cloudv1.PreviewTarget{Name: "feat-cart-1a2b3c4d", Label: "pr-12", Persistent: true}
	requireAccepted(t, "a preview up", preview)

	nested := validStartBuild()
	nested.ProjectPath = "apps/web"
	requireAccepted(t, "a project below the source root", nested)

	requireAccepted(t, "a job read", &cloudv1.GetJobRequest{JobId: "job_01JABCDEF"})
	requireAccepted(t, "a job cancel", &cloudv1.CancelJobRequest{JobId: "job_01JABCDEF"})
	requireAccepted(t, "an event page", &cloudv1.ListJobEventsRequest{JobId: "job_01JABCDEF", AfterSeq: 40, Limit: 1000})
}

func TestProtovalidateRefusesAStartThatBreaksAJobRule(t *testing.T) {
	requireRefusals(t, validStartBuild, []refusal[*cloudv1.StartJobRequest]{
		{name: "a project id is required", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectId = "" }},
		{name: "a project id is URL-safe", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectId = "../org_2" }},
		{name: "a slug is required", mutate: func(r *cloudv1.StartJobRequest) { r.Slug = "" }},
		{name: "a slug is a DNS label", mutate: func(r *cloudv1.StartJobRequest) { r.Slug = "Shop_1" }},
		{name: "a slug has no double hyphen", mutate: func(r *cloudv1.StartJobRequest) { r.Slug = "shop--web" }},
		{name: "a slug is at most 63 characters", mutate: func(r *cloudv1.StartJobRequest) { r.Slug = strings.Repeat("a", 64) }},
		{name: "a command is named", mutate: func(r *cloudv1.StartJobRequest) { r.Command = cloudv1.JobCommand_JOB_COMMAND_UNSPECIFIED }},
		{name: "a command is one this contract defines", mutate: func(r *cloudv1.StartJobRequest) { r.Command = 99 }},
		{name: "a source id is a sha256", mutate: func(r *cloudv1.StartJobRequest) { r.SourceId = "src_1" }},
		{name: "a CLI version is required", mutate: func(r *cloudv1.StartJobRequest) { r.CliVersion = "" }},
		{name: "a CLI version is no flag", mutate: func(r *cloudv1.StartJobRequest) { r.CliVersion = "--debug" }},
		{name: "a CLI version has no space", mutate: func(r *cloudv1.StartJobRequest) { r.CliVersion = "1.0 --yes" }},
		{name: "a commit is hex", mutate: func(r *cloudv1.StartJobRequest) { r.Git.Commit = "HEAD" }},
		{name: "a branch has no control character", mutate: func(r *cloudv1.StartJobRequest) { r.Git.Branch = "main\n--yes" }},
		{name: "a branch is at most 256 characters", mutate: func(r *cloudv1.StartJobRequest) { r.Git.Branch = strings.Repeat("b", 257) }},
		{name: "a project path is relative", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectPath = "/apps/web" }, rule: "start_job.project_path"},
		{name: "a project path does not climb", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectPath = "apps/../.." }, rule: "start_job.project_path"},
		{name: "a project path has no trailing slash", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectPath = "apps/web/" }, rule: "start_job.project_path"},
		{name: "a project path is slash-separated", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectPath = `apps\web` }, rule: "start_job.project_path"},
		{name: "a project path has no control character", mutate: func(r *cloudv1.StartJobRequest) { r.ProjectPath = "apps/w\neb" }, rule: "start_job.project_path"},
		{name: "a tag is given only to a deploy", mutate: func(r *cloudv1.StartJobRequest) { r.Tag = "v1" }, rule: "start_job.tag_only_for_deploy"},
		{name: "a tag is no flag", mutate: func(r *cloudv1.StartJobRequest) {
			r.Command, r.Tag = cloudv1.JobCommand_JOB_COMMAND_DEPLOY, "v1 --prebuilt"
		}},
		{name: "a preview target is given only to a preview up", mutate: func(r *cloudv1.StartJobRequest) {
			r.Preview = &cloudv1.PreviewTarget{Name: "pr-12"}
		}, rule: "start_job.preview_only_for_preview_up"},
		{name: "a preview up names its target", mutate: func(r *cloudv1.StartJobRequest) {
			r.Command = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP
		}, rule: "start_job.preview_only_for_preview_up"},
		{name: "a preview name is a DNS label", mutate: func(r *cloudv1.StartJobRequest) {
			r.Command, r.Preview = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &cloudv1.PreviewTarget{Name: "--persistent"}
		}},
		{name: "a preview name has no double hyphen", mutate: func(r *cloudv1.StartJobRequest) {
			r.Command, r.Preview = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &cloudv1.PreviewTarget{Name: "pr--12"}
		}},
		{name: "a preview label is a DNS label", mutate: func(r *cloudv1.StartJobRequest) {
			r.Command, r.Preview = cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &cloudv1.PreviewTarget{Name: "feat-cart-1a2b3c4d", Label: "PR 12"}
		}},
	})
}

func TestProtovalidateRefusesAJobIDThatIsNotURLSafe(t *testing.T) {
	for _, id := range []string{"", "job/../other", "job 1", strings.Repeat("j", 129)} {
		requireRefusals(t, func() *cloudv1.GetJobRequest { return &cloudv1.GetJobRequest{JobId: "job_1"} }, []refusal[*cloudv1.GetJobRequest]{
			{name: "get " + id, mutate: func(r *cloudv1.GetJobRequest) { r.JobId = id }},
		})
		requireRefusals(t, func() *cloudv1.CancelJobRequest { return &cloudv1.CancelJobRequest{JobId: "job_1"} }, []refusal[*cloudv1.CancelJobRequest]{
			{name: "cancel " + id, mutate: func(r *cloudv1.CancelJobRequest) { r.JobId = id }},
		})
		requireRefusals(t, func() *cloudv1.ListJobEventsRequest { return &cloudv1.ListJobEventsRequest{JobId: "job_1"} }, []refusal[*cloudv1.ListJobEventsRequest]{
			{name: "list events " + id, mutate: func(r *cloudv1.ListJobEventsRequest) { r.JobId = id }},
		})
	}
	requireRefusals(t, func() *cloudv1.ListJobEventsRequest { return &cloudv1.ListJobEventsRequest{JobId: "job_1"} }, []refusal[*cloudv1.ListJobEventsRequest]{
		{name: "a page holds at most 1000 events", mutate: func(r *cloudv1.ListJobEventsRequest) { r.Limit = 1001 }},
	})
}

func validVariablesInput() *cloudv1.JobInput {
	return &cloudv1.JobInput{Id: "in_0a1b2c3d", Need: &cloudv1.JobInput_Variables{Variables: &cloudv1.VariablesNeeded{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Variables: []*cloudv1.NeededVariable{
			{Key: "DATABASE_URL", Secret: true, Description: "Where orders are kept.", Reason: "no value", Group: "db"},
			{Key: "API_BASE", Folder: "/apps/web", Reason: "no value"},
		},
		Groups: []*cloudv1.NeededGroup{{Key: "db", Description: "The database."}},
	}}}
}

func validRespondWithValues() *cloudv1.RespondJobRequest {
	return &cloudv1.RespondJobRequest{JobId: "job_1", InputId: "in_0a1b2c3d", Answer: &cloudv1.RespondJobRequest_Variables{Variables: &cloudv1.VariableValues{
		Values: []*cloudv1.VariableValue{{Key: "DATABASE_URL", Value: "postgres://db"}, {Key: "API_BASE", Folder: "/apps/web", Value: ""}},
	}}}
}

func TestProtovalidateAcceptsEveryInputAJobAsksForAndEveryAnswer(t *testing.T) {
	requireAccepted(t, "missing variables", validVariablesInput())
	requireAccepted(t, "a confirmation", &cloudv1.JobInput{Id: "in_1", Need: &cloudv1.JobInput_Confirmation{Confirmation: &cloudv1.Confirmation{Finding: "This will create a NEW project.", Question: "Continue?"}}})
	requireAccepted(t, "values", validRespondWithValues())
	requireAccepted(t, "a declined confirmation", &cloudv1.RespondJobRequest{JobId: "job_1", InputId: "in_1", Answer: &cloudv1.RespondJobRequest_Confirmed{Confirmed: false}})
}

func TestProtovalidateRefusesAnInputOrAnswerThatBreaksAJobRule(t *testing.T) {
	requireRefusals(t, validVariablesInput, []refusal[*cloudv1.JobInput]{
		{name: "an input has an id", mutate: func(r *cloudv1.JobInput) { r.Id = "" }},
		{name: "an input id is URL-safe", mutate: func(r *cloudv1.JobInput) { r.Id = "in/../1" }},
		{name: "an input names what it needs", mutate: func(r *cloudv1.JobInput) { r.Need = nil }},
		{name: "variables name their tier", mutate: func(r *cloudv1.JobInput) { r.GetVariables().Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "variables name at least one", mutate: func(r *cloudv1.JobInput) { r.GetVariables().Variables = nil }},
		{name: "a variable has a key", mutate: func(r *cloudv1.JobInput) { r.GetVariables().Variables[0].Key = "" }},
		{name: "a variable's folder is absolute", mutate: func(r *cloudv1.JobInput) { r.GetVariables().Variables[1].Folder = "apps/web" }},
		{name: "a confirmation asks a question", mutate: func(r *cloudv1.JobInput) {
			r.Need = &cloudv1.JobInput_Confirmation{Confirmation: &cloudv1.Confirmation{Finding: "This will create a NEW project."}}
		}},
	})
	requireRefusals(t, validRespondWithValues, []refusal[*cloudv1.RespondJobRequest]{
		{name: "an answer names its job", mutate: func(r *cloudv1.RespondJobRequest) { r.JobId = "" }},
		{name: "an answer names the input it answers", mutate: func(r *cloudv1.RespondJobRequest) { r.InputId = "" }},
		{name: "an answer answers", mutate: func(r *cloudv1.RespondJobRequest) { r.Answer = nil }},
		{name: "values hold at least one", mutate: func(r *cloudv1.RespondJobRequest) { r.GetVariables().Values = nil }},
		{name: "a value has a key", mutate: func(r *cloudv1.RespondJobRequest) { r.GetVariables().Values[0].Key = "" }},
		{name: "a value's key has no control character", mutate: func(r *cloudv1.RespondJobRequest) { r.GetVariables().Values[0].Key = "DB\nURL" }},
		{name: "a value's folder has no fragment", mutate: func(r *cloudv1.RespondJobRequest) { r.GetVariables().Values[1].Folder = "/apps/web#x" }},
	})
}
