package runnerv1_test

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

const projectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

func validStartBuild() *runnerv1.JobRequest {
	return &runnerv1.JobRequest{
		Command: runnerv1.JobCommand_JOB_COMMAND_BUILD,
		Source:  &runnerv1.JobRequest_SourceId{SourceId: digest},
		Git:     &runnerv1.GitContext{Commit: "0a1b2c3d4e5f", Branch: "feat/cart", Dirty: true, PullRequest: 12},
	}
}

func TestProtovalidateAcceptsEveryJobRequestAConsoleCanDispatch(t *testing.T) {
	requireAccepted(t, "a build", validStartBuild())
	requireAccepted(t, "a build outside git", &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, Source: &runnerv1.JobRequest_SourceId{SourceId: digest}})
	requireAccepted(t, "a build from git", &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, Source: &runnerv1.JobRequest_GitSource{GitSource: &runnerv1.GitSource{
		CloneUrl: "https://github.com/example/shop.git", Commit: strings.Repeat("0a", 20), Token: "installation-token",
	}}})
	requireAccepted(t, "a start", &runnerv1.StartJobRequest{Job: validStartBuild()})

	deploy := validStartBuild()
	deploy.Command, deploy.Tag = runnerv1.JobCommand_JOB_COMMAND_DEPLOY, "v1.2.0"
	requireAccepted(t, "a tagged deploy", deploy)

	preview := validStartBuild()
	preview.Command, preview.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &runnerv1.PreviewTarget{Name: "feat-cart-1a2b3c4d", Label: "pr-12", Persistent: true}
	requireAccepted(t, "a preview up", preview)

	removal := validStartBuild()
	removal.Command, removal.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, &runnerv1.PreviewTarget{Name: "feat-cart-1a2b3c4d"}
	requireAccepted(t, "a preview rm", removal)

	nested := validStartBuild()
	nested.ProjectPath = "apps/web"
	requireAccepted(t, "a project below the source root", nested)

	configured := validStartBuild()
	configured.ConfigFile = "ocel.staging.json"
	requireAccepted(t, "a job run with a config file the project holds", configured)

	requireAccepted(t, "a job read", &runnerv1.GetJobRequest{JobId: "job_01JABCDEF"})
	requireAccepted(t, "a job cancel", &runnerv1.CancelJobRequest{JobId: "job_01JABCDEF"})
	requireAccepted(t, "an event page", &runnerv1.ListJobEventsRequest{JobId: "job_01JABCDEF", AfterSeq: 40, Limit: 1000})
}

func TestProtovalidateRefusesAJobRequestThatBreaksAJobRule(t *testing.T) {
	requireRefusals(t, validStartBuild, []refusal[*runnerv1.JobRequest]{
		{name: "a command is named", mutate: func(r *runnerv1.JobRequest) { r.Command = runnerv1.JobCommand_JOB_COMMAND_UNSPECIFIED }},
		{name: "a command is one this contract defines", mutate: func(r *runnerv1.JobRequest) { r.Command = 99 }},
		{name: "a source id is a sha256", mutate: func(r *runnerv1.JobRequest) { r.Source = &runnerv1.JobRequest_SourceId{SourceId: "src_1"} }},
		{name: "a job names its source", mutate: func(r *runnerv1.JobRequest) { r.Source = nil }},
		{name: "a git source clones over https", mutate: func(r *runnerv1.JobRequest) {
			r.Source = &runnerv1.JobRequest_GitSource{GitSource: &runnerv1.GitSource{CloneUrl: "git://github.com/example/shop.git", Commit: strings.Repeat("0a", 20), Token: "t"}}
		}, rule: "git_source.clone_url_https"},
		{name: "a git source names a full commit", mutate: func(r *runnerv1.JobRequest) {
			r.Source = &runnerv1.JobRequest_GitSource{GitSource: &runnerv1.GitSource{CloneUrl: "https://github.com/example/shop.git", Commit: "0a1b2c3", Token: "t"}}
		}},
		{name: "a commit is hex", mutate: func(r *runnerv1.JobRequest) { r.Git.Commit = "HEAD" }},
		{name: "a branch has no control character", mutate: func(r *runnerv1.JobRequest) { r.Git.Branch = "main\n--yes" }},
		{name: "a branch is at most 256 characters", mutate: func(r *runnerv1.JobRequest) { r.Git.Branch = strings.Repeat("b", 257) }},
		{name: "a project path is relative", mutate: func(r *runnerv1.JobRequest) { r.ProjectPath = "/apps/web" }, rule: "job_request.project_path"},
		{name: "a project path does not climb", mutate: func(r *runnerv1.JobRequest) { r.ProjectPath = "apps/../.." }, rule: "job_request.project_path"},
		{name: "a project path has no trailing slash", mutate: func(r *runnerv1.JobRequest) { r.ProjectPath = "apps/web/" }, rule: "job_request.project_path"},
		{name: "a project path is slash-separated", mutate: func(r *runnerv1.JobRequest) { r.ProjectPath = `apps\web` }, rule: "job_request.project_path"},
		{name: "a project path has no control character", mutate: func(r *runnerv1.JobRequest) { r.ProjectPath = "apps/w\neb" }, rule: "job_request.project_path"},
		{name: "a config file is relative", mutate: func(r *runnerv1.JobRequest) { r.ConfigFile = "/etc/ocel.json" }, rule: "job_request.config_file"},
		{name: "a config file does not climb out of the project", mutate: func(r *runnerv1.JobRequest) { r.ConfigFile = "../ocel.json" }, rule: "job_request.config_file"},
		{name: "a config file is slash-separated", mutate: func(r *runnerv1.JobRequest) { r.ConfigFile = `..\ocel.json` }, rule: "job_request.config_file"},
		{name: "a config file has no control character", mutate: func(r *runnerv1.JobRequest) { r.ConfigFile = "ocel.json\n--yes" }, rule: "job_request.config_file"},
		{name: "a tag is given only to a deploy", mutate: func(r *runnerv1.JobRequest) { r.Tag = "v1" }, rule: "job_request.tag_only_for_deploy"},
		{name: "a tag is no flag", mutate: func(r *runnerv1.JobRequest) {
			r.Command, r.Tag = runnerv1.JobCommand_JOB_COMMAND_DEPLOY, "v1 --prebuilt"
		}},
		{name: "a preview target is given only to a preview command", mutate: func(r *runnerv1.JobRequest) {
			r.Preview = &runnerv1.PreviewTarget{Name: "pr-12"}
		}, rule: "job_request.preview_only_for_preview_commands"},
		{name: "a preview up names its target", mutate: func(r *runnerv1.JobRequest) {
			r.Command = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP
		}, rule: "job_request.preview_only_for_preview_commands"},
		{name: "a preview name is a DNS label", mutate: func(r *runnerv1.JobRequest) {
			r.Command, r.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &runnerv1.PreviewTarget{Name: "--persistent"}
		}},
		{name: "a preview name has no double hyphen", mutate: func(r *runnerv1.JobRequest) {
			r.Command, r.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &runnerv1.PreviewTarget{Name: "pr--12"}
		}},
		{name: "a preview label is a DNS label", mutate: func(r *runnerv1.JobRequest) {
			r.Command, r.Preview = runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, &runnerv1.PreviewTarget{Name: "feat-cart-1a2b3c4d", Label: "PR 12"}
		}},
	})
}

func TestProtovalidateRefusesAJobIDThatIsNotURLSafe(t *testing.T) {
	for _, id := range []string{"", "job/../other", "job 1", strings.Repeat("j", 129)} {
		requireRefusals(t, func() *runnerv1.GetJobRequest { return &runnerv1.GetJobRequest{JobId: "job_1"} }, []refusal[*runnerv1.GetJobRequest]{
			{name: "get " + id, mutate: func(r *runnerv1.GetJobRequest) { r.JobId = id }},
		})
		requireRefusals(t, func() *runnerv1.CancelJobRequest { return &runnerv1.CancelJobRequest{JobId: "job_1"} }, []refusal[*runnerv1.CancelJobRequest]{
			{name: "cancel " + id, mutate: func(r *runnerv1.CancelJobRequest) { r.JobId = id }},
		})
		requireRefusals(t, func() *runnerv1.ListJobEventsRequest { return &runnerv1.ListJobEventsRequest{JobId: "job_1"} }, []refusal[*runnerv1.ListJobEventsRequest]{
			{name: "list events " + id, mutate: func(r *runnerv1.ListJobEventsRequest) { r.JobId = id }},
		})
	}
	requireRefusals(t, func() *runnerv1.ListJobEventsRequest { return &runnerv1.ListJobEventsRequest{JobId: "job_1"} }, []refusal[*runnerv1.ListJobEventsRequest]{
		{name: "a page holds at most 1000 events", mutate: func(r *runnerv1.ListJobEventsRequest) { r.Limit = 1001 }},
	})
}

func validVariablesInput() *runnerv1.JobInput {
	return &runnerv1.JobInput{Id: "in_0a1b2c3d", Need: &runnerv1.JobInput_Variables{Variables: &runnerv1.VariablesNeeded{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Variables: []*runnerv1.NeededVariable{
			{Key: "DATABASE_URL", Secret: true, Description: "Where orders are kept.", Reason: "no value", Group: "db"},
			{Key: "API_BASE", Folder: "/apps/web", Reason: "no value"},
		},
		Groups: []*runnerv1.NeededGroup{{Key: "db", Description: "The database."}},
	}}}
}

func validSupplyVariables() *runnerv1.SupplyVariablesRequest {
	return &runnerv1.SupplyVariablesRequest{JobId: "job_1", InputId: "in_0a1b2c3d", Variables: &runnerv1.VariableValues{
		Values: []*runnerv1.VariableValue{{Key: "DATABASE_URL", Value: "postgres://db"}, {Key: "API_BASE", Folder: "/apps/web", Value: ""}},
	}}
}

func TestProtovalidateAcceptsEveryInputAJobAsksForAndEveryAnswer(t *testing.T) {
	requireAccepted(t, "missing variables", validVariablesInput())
	requireAccepted(t, "a confirmation", &runnerv1.JobInput{Id: "in_1", Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Finding: "This will create a NEW project.", Question: "Continue?"}}})
	requireAccepted(t, "values", validSupplyVariables())
	requireAccepted(t, "a declined confirmation", &runnerv1.AnswerConfirmationRequest{JobId: "job_1", InputId: "in_1", Confirmed: false})
}

func TestProtovalidateRefusesAnInputOrAnswerThatBreaksAJobRule(t *testing.T) {
	requireRefusals(t, validVariablesInput, []refusal[*runnerv1.JobInput]{
		{name: "an input has an id", mutate: func(r *runnerv1.JobInput) { r.Id = "" }},
		{name: "an input id is URL-safe", mutate: func(r *runnerv1.JobInput) { r.Id = "in/../1" }},
		{name: "an input names what it needs", mutate: func(r *runnerv1.JobInput) { r.Need = nil }},
		{name: "variables name their tier", mutate: func(r *runnerv1.JobInput) { r.GetVariables().Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "variables name at least one", mutate: func(r *runnerv1.JobInput) { r.GetVariables().Variables = nil }},
		{name: "a variable has a key", mutate: func(r *runnerv1.JobInput) { r.GetVariables().Variables[0].Key = "" }},
		{name: "a variable's folder is absolute", mutate: func(r *runnerv1.JobInput) { r.GetVariables().Variables[1].Folder = "apps/web" }},
		{name: "a confirmation asks a question", mutate: func(r *runnerv1.JobInput) {
			r.Need = &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Finding: "This will create a NEW project."}}
		}},
	})
	requireRefusals(t, validSupplyVariables, []refusal[*runnerv1.SupplyVariablesRequest]{
		{name: "an answer names its job", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.JobId = "" }},
		{name: "an answer names the input it answers", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.InputId = "" }},
		{name: "an answer carries values", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.Variables = nil }},
		{name: "values hold at least one", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.Variables.Values = nil }},
		{name: "a value has a key", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.Variables.Values[0].Key = "" }},
		{name: "a value's key has no control character", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.Variables.Values[0].Key = "DB\nURL" }},
		{name: "a value's folder has no fragment", mutate: func(r *runnerv1.SupplyVariablesRequest) { r.Variables.Values[1].Folder = "/apps/web#x" }},
	})
	requireRefusals(t, func() *runnerv1.AnswerConfirmationRequest {
		return &runnerv1.AnswerConfirmationRequest{JobId: "job_1", InputId: "in_1", Confirmed: true}
	}, []refusal[*runnerv1.AnswerConfirmationRequest]{
		{name: "a confirmation names its job", mutate: func(r *runnerv1.AnswerConfirmationRequest) { r.JobId = "" }},
		{name: "a confirmation names the input it answers", mutate: func(r *runnerv1.AnswerConfirmationRequest) { r.InputId = "" }},
	})
}

func validJobContext() *runnerv1.JobContext {
	return &runnerv1.JobContext{
		JobId:        "job_01JABCDEF",
		ProjectId:    projectID,
		Tier:         environmentv1.Tier_TIER_PREVIEW,
		DeploymentId: strings.Repeat("0a", 16),
		Slug:         "shop",
		Git:          &runnerv1.GitContext{Commit: "0a1b2c3d4e5f", Branch: "feat/cart", Dirty: true, PullRequest: 12},
		PreviewLabel: "pr-12",
		ConsoleUrl:   "https://console.example.com",
		JobTokenFile: "/workspace/job/token",
	}
}

func TestProtovalidateAcceptsEveryJobContextAnAgentCanWrite(t *testing.T) {
	requireAccepted(t, "a preview's job", validJobContext())
	requireAccepted(t, "a job outside git", &runnerv1.JobContext{JobId: "job_01JABCDEF", ProjectId: projectID, Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop", ConsoleUrl: "https://console.example.com", JobTokenFile: "/workspace/job/token"})
}

func TestProtovalidateRefusesAJobContextThatBreaksAJobRule(t *testing.T) {
	requireRefusals(t, validJobContext, []refusal[*runnerv1.JobContext]{
		{name: "a job id is required", mutate: func(r *runnerv1.JobContext) { r.JobId = "" }},
		{name: "a job id is URL-safe", mutate: func(r *runnerv1.JobContext) { r.JobId = "job/../2" }},
		{name: "a slug is required", mutate: func(r *runnerv1.JobContext) { r.Slug = "" }},
		{name: "a slug is a DNS label", mutate: func(r *runnerv1.JobContext) { r.Slug = "Shop_1" }},
		{name: "a commit is hex", mutate: func(r *runnerv1.JobContext) { r.Git.Commit = "HEAD" }},
		{name: "a preview label is a DNS label", mutate: func(r *runnerv1.JobContext) { r.PreviewLabel = "PR 12" }},
		{name: "a project id is a uuid", mutate: func(r *runnerv1.JobContext) { r.ProjectId = "shop" }},
		{name: "a tier is named", mutate: func(r *runnerv1.JobContext) { r.Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "a deployment id is a trace id", mutate: func(r *runnerv1.JobContext) { r.DeploymentId = "dep_1" }},
		{name: "a console is named", mutate: func(r *runnerv1.JobContext) { r.ConsoleUrl = "" }},
		{name: "a job token file is named", mutate: func(r *runnerv1.JobContext) { r.JobTokenFile = "" }},
	})
}
