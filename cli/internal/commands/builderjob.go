package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	CloudJobEnvVar            = "OCEL_CLOUD_JOB"
	CloudSlugEnvVar           = "OCEL_CLOUD_SLUG"
	CloudPreviewLabelEnvVar   = "OCEL_CLOUD_PREVIEW_LABEL"
	CloudGitCommitEnvVar      = "OCEL_CLOUD_GIT_COMMIT"
	CloudGitBranchEnvVar      = "OCEL_CLOUD_GIT_BRANCH"
	CloudGitDirtyEnvVar       = "OCEL_CLOUD_GIT_DIRTY"
	CloudGitPullRequestEnvVar = "OCEL_CLOUD_GIT_PR"
	CloudProviderID           = "ocel"
)

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

type BuilderJob struct {
	ID           string
	Slug         string
	ProvidersDir string
	PreviewLabel string

	GitCommit      string
	GitBranch      string
	GitDirty       string
	GitPullRequest string
}

func ReadBuilderJob(getenv func(string) string) *BuilderJob {
	id := getenv(CloudJobEnvVar)
	if id == "" {
		return nil
	}
	return &BuilderJob{
		ID: id, Slug: getenv(CloudSlugEnvVar), ProvidersDir: getenv(executables.OverrideEnvVar),
		PreviewLabel: getenv(CloudPreviewLabelEnvVar),
		GitCommit:    getenv(CloudGitCommitEnvVar), GitBranch: getenv(CloudGitBranchEnvVar), GitDirty: getenv(CloudGitDirtyEnvVar), GitPullRequest: getenv(CloudGitPullRequestEnvVar),
	}
}

func (j *BuilderJob) Source() *consolev1.Source {
	if j.GitCommit == "" {
		return nil
	}
	dirty, _ := strconv.ParseBool(j.GitDirty)
	return &consolev1.Source{Commit: j.GitCommit, Branch: j.GitBranch, Dirty: dirty}
}

func (j *BuilderJob) ReadGitBranch(string) (string, error) {
	if j.GitBranch == "" {
		return "", fmt.Errorf("job %s on Ocel Cloud was started with no git branch in %s, so no branch names this preview: name it, as in `ocel preview up <name>`", j.ID, CloudGitBranchEnvVar)
	}
	return j.GitBranch, nil
}

func (j *BuilderJob) DiscoverPRNumber() string { return j.GitPullRequest }

func (j *BuilderJob) loadProject(ctx context.Context, cwd, explicitPath string) (*project.Project, error) {
	configPath, err := project.FindConfig(cwd, explicitPath)
	if err != nil {
		return nil, err
	}
	if configPath != "" {
		cfg, err := project.Load(ctx, cwd, explicitPath)
		if err != nil {
			return nil, err
		}
		return j.adopt(cfg)
	}
	dir, err := project.FindRoot(cwd, explicitPath)
	if err != nil {
		return nil, err
	}
	return j.adopt(project.NewUnconfigured(dir, j.Slug))
}

func (j *BuilderJob) adopt(cfg *project.Project) (*project.Project, error) {
	if err := j.refuseInvalid(); err != nil {
		return nil, err
	}
	if cfg.HasProvider() {
		return nil, fmt.Errorf("this runs inside Ocel Cloud job %s, and %s names the provider %q: a project that deploys to Ocel Cloud names none", j.ID, filepath.Base(cfg.Path), cfg.Provider.ID)
	}
	if cfg.Slug != j.Slug {
		return nil, fmt.Errorf("job %s on Ocel Cloud was started for the project %s, and %s names %s", j.ID, j.Slug, filepath.Base(cfg.Path), cfg.Slug)
	}
	cfg.Provider = &project.Provider{ID: CloudProviderID}
	return cfg, nil
}

func (j *BuilderJob) refuseInvalid() error {
	if err := project.ValidateSlug(j.Slug); err != nil {
		return fmt.Errorf("%s is set, so this runs inside an Ocel Cloud job, and %s does not name the project's slug: %w", CloudJobEnvVar, CloudSlugEnvVar, err)
	}
	if j.ProvidersDir == "" {
		return fmt.Errorf("%s is set, so this runs inside an Ocel Cloud job, which spawns the provider %s from %s, and %s is not set", CloudJobEnvVar, CloudProviderID, executables.OverrideEnvVar, executables.OverrideEnvVar)
	}
	if j.PreviewLabel != "" {
		if err := previewid.ValidateLabel(j.PreviewLabel); err != nil {
			return fmt.Errorf("%s does not name the preview's label: %w", CloudPreviewLabelEnvVar, err)
		}
	}
	if j.GitCommit != "" && !gitCommitPattern.MatchString(j.GitCommit) {
		return fmt.Errorf("%s is %q, which is not a git commit: give it the commit's hex object name", CloudGitCommitEnvVar, j.GitCommit)
	}
	if _, err := strconv.ParseBool(j.GitDirty); j.GitDirty != "" && err != nil {
		return fmt.Errorf("%s is %q, which is neither true nor false", CloudGitDirtyEnvVar, j.GitDirty)
	}
	if _, err := strconv.ParseUint(j.GitPullRequest, 10, 32); j.GitPullRequest != "" && err != nil {
		return fmt.Errorf("%s is %q, which is not a pull request's number", CloudGitPullRequestEnvVar, j.GitPullRequest)
	}
	return nil
}
