package clitest

import (
	"fmt"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func DescribeDispatched(requests []*consolev1.DispatchJobRequest) []string {
	described := make([]string, len(requests))
	for i, request := range requests {
		described[i] = DescribeDispatch(request)
	}
	return described
}

func DescribeDispatch(request *consolev1.DispatchJobRequest) string {
	return fmt.Sprintf("{request %s, project %s, runner %s, slug %s, provider %q, tier %s, job %s}",
		request.GetRequestId(), request.GetProjectId(), request.GetRunnerId(), request.GetSlug(), request.GetProvider(), request.GetTier(), DescribeJob(request.GetJob()))
}

func DescribeJob(job *runnerv1.JobRequest) string {
	source := "source " + job.GetSourceId()
	if git := job.GetGitSource(); git != nil {
		source = "git " + git.GetCloneUrl() + "@" + git.GetCommit()
	}
	return fmt.Sprintf("{%s, %s, project path %q, config file %q, tag %q, preview %q persistent %t, tier values %t}",
		job.GetCommand(), source, job.GetProjectPath(), job.GetConfigFile(), job.GetTag(), job.GetPreview().GetName(), job.GetPreview().GetPersistent(), job.GetTierValues())
}

func DescribeSupplied(requests []*runnerv1.SupplyVariablesRequest) []string {
	described := make([]string, len(requests))
	for i, request := range requests {
		described[i] = fmt.Sprintf("{job %s, input %s, values %q}", request.GetJobId(), request.GetInputId(), DescribeValueKeys(request.GetVariables().GetValues()))
	}
	return described
}

func DescribeValueKeys(values []*runnerv1.VariableValue) []string {
	keys := make([]string, len(values))
	for i, value := range values {
		keys[i] = value.GetFolder() + "/" + value.GetKey()
	}
	return keys
}
