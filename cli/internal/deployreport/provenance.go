package deployreport

import (
	"context"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/githubactions"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	detachedHead      = "HEAD"
	githubActionsName = "github-actions"
	unknownCIName     = "ci"
	runnerName        = "ocel-runner"
)

func ReadRunnerTrigger(prNumber string) (*consolev1.Trigger, *consolev1.CI) {
	ci := &consolev1.CI{Name: runnerName}
	if pr, err := strconv.ParseUint(prNumber, 10, 32); err == nil {
		ci.Pr = uint32(pr)
	}
	return &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CI}, ci
}

func ReadTrigger(getenv func(string) string, prNumber string) (*consolev1.Trigger, *consolev1.CI) {
	if !githubactions.IsRunning(getenv) && getenv("CI") == "" {
		return &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CLI}, nil
	}
	trigger := &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CI}
	if !githubactions.IsRunning(getenv) {
		return trigger, &consolev1.CI{Name: unknownCIName}
	}
	trigger.Actor = getenv("GITHUB_ACTOR")
	ci := &consolev1.CI{Name: githubActionsName, Repo: getenv("GITHUB_REPOSITORY")}
	if repo, run := getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID"); repo != "" && run != "" && getenv("GITHUB_SERVER_URL") != "" {
		ci.RunUrl = getenv("GITHUB_SERVER_URL") + "/" + repo + "/actions/runs/" + run
	}
	if pr, err := strconv.ParseUint(prNumber, 10, 32); err == nil {
		ci.Pr = uint32(pr)
	}
	return trigger, ci
}

func ReadSource(ctx context.Context, dir string, getenv func(string) string) *consolev1.Source {
	commit, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil
	}
	source := &consolev1.Source{Commit: commit}
	if branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && branch != detachedHead {
		source.Branch = branch
	} else {
		source.Branch = getenv("GITHUB_HEAD_REF")
	}
	if status, err := git(ctx, dir, "status", "--porcelain", "--untracked-files=no"); err == nil {
		source.Dirty = status != ""
	}
	return source
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := tracing.Command(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
