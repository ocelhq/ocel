package agent

import (
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func Args(job *runnerv1.JobRequest, tier environmentv1.Tier) []string {
	args := commandArgs(job, tier)
	if len(args) == 0 || job.GetConfigFile() == "" {
		return args
	}
	return append(args, "--config", job.GetConfigFile())
}

func commandArgs(job *runnerv1.JobRequest, tier environmentv1.Tier) []string {
	switch job.GetCommand() {
	case runnerv1.JobCommand_JOB_COMMAND_BUILD:
		if !job.GetTierValues() {
			return []string{"build", "--json"}
		}
		switch tier {
		case environmentv1.Tier_TIER_PRODUCTION:
			return []string{"build", "--env", "production", "--json"}
		case environmentv1.Tier_TIER_PREVIEW:
			return []string{"build", "--env", "preview", "--json"}
		}
	case runnerv1.JobCommand_JOB_COMMAND_DEPLOY:
		args := []string{"deploy", "--json"}
		if job.GetTag() != "" {
			args = append(args, "--tag", job.GetTag())
		}
		return args
	case runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP:
		args := []string{"preview", "up", job.GetPreview().GetName(), "--json"}
		if job.GetPreview().GetPersistent() {
			args = append(args, "--persistent")
		}
		return args
	case runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM:
		return []string{"preview", "rm", job.GetPreview().GetName(), "--json"}
	case runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY:
		switch tier {
		case environmentv1.Tier_TIER_PRODUCTION:
			return []string{"projects", "destroy", "--tier", "production", "--json"}
		case environmentv1.Tier_TIER_PREVIEW:
			return []string{"projects", "destroy", "--tier", "preview", "--json"}
		}
	}
	return nil
}
