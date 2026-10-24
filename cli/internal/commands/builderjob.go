package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	CloudJobContextEnvVar = "OCEL_CLOUD_JOB_CONTEXT"
	CloudProviderID       = "ocel"
)

type BuilderJob struct {
	ID           string
	Slug         string
	ProvidersDir string
	PreviewLabel string
	Git          *cloudv1.GitContext
}

func ReadBuilderJob(getenv func(string) string) (*BuilderJob, error) {
	path := getenv(CloudJobContextEnvVar)
	if path == "" {
		return nil, nil
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, refuseJobContext(fmt.Errorf("%s is set, so this runs inside an Ocel Cloud job, and its context could not be read: %w", CloudJobContextEnvVar, err))
	}
	var job cloudv1.JobContext
	if err := protojson.Unmarshal(encoded, &job); err != nil {
		return nil, refuseJobContext(fmt.Errorf("%s, named by %s, is not an Ocel Cloud job's context: %w", path, CloudJobContextEnvVar, err))
	}
	if err := protovalidate.Validate(&job); err != nil {
		return nil, refuseJobContext(fmt.Errorf("%s, named by %s, holds an Ocel Cloud job's context that is not valid: %w", path, CloudJobContextEnvVar, err))
	}
	providers := getenv(executables.OverrideEnvVar)
	if providers == "" {
		return nil, refuseJobContext(fmt.Errorf("this runs inside Ocel Cloud job %s, which spawns the provider %s from %s, and %s is not set", job.GetJobId(), CloudProviderID, executables.OverrideEnvVar, executables.OverrideEnvVar))
	}
	return &BuilderJob{ID: job.GetJobId(), Slug: job.GetSlug(), ProvidersDir: providers, PreviewLabel: job.GetPreviewLabel(), Git: job.GetGit()}, nil
}

func refuseJobContext(err error) error {
	return &clierror.Error{Code: clierror.CodeCloudJobContextInvalid, Cause: err}
}

func (j *BuilderJob) Source() *consolev1.Source {
	if j.Git.GetCommit() == "" {
		return nil
	}
	return &consolev1.Source{Commit: j.Git.GetCommit(), Branch: j.Git.GetBranch(), Dirty: j.Git.GetDirty()}
}

func (j *BuilderJob) ReadGitBranch(string) (string, error) {
	if j.Git.GetBranch() == "" {
		return "", fmt.Errorf("job %s on Ocel Cloud has no git branch to name this preview: run `ocel preview up <name>`", j.ID)
	}
	return j.Git.GetBranch(), nil
}

func (j *BuilderJob) DiscoverPRNumber() string {
	if j.Git.GetPullRequest() == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(j.Git.GetPullRequest()), 10)
}

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
	if cfg.HasProvider() {
		return nil, &clierror.Error{Code: clierror.CodeProjectInvalidConfig, Hint: "provider",
			Cause: fmt.Errorf("this runs inside Ocel Cloud job %s, and %s names the provider %q: remove it", j.ID, filepath.Base(cfg.Path), cfg.Provider.ID)}
	}
	if cfg.Slug != j.Slug {
		return nil, &clierror.Error{Code: clierror.CodeProjectInvalidConfig, Hint: "slug",
			Cause: fmt.Errorf("job %s on Ocel Cloud was started for the project %s, and %s names %s", j.ID, j.Slug, filepath.Base(cfg.Path), cfg.Slug)}
	}
	cfg.Provider = &project.Provider{ID: CloudProviderID}
	return cfg, nil
}
