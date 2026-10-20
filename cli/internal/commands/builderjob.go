package commands

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
)

const (
	CloudJobEnvVar  = "OCEL_CLOUD_JOB"
	CloudSlugEnvVar = "OCEL_CLOUD_SLUG"
	CloudProviderID = "ocel"
)

type BuilderJob struct {
	ID           string
	Slug         string
	ProvidersDir string
}

func ReadBuilderJob(getenv func(string) string) *BuilderJob {
	id := getenv(CloudJobEnvVar)
	if id == "" {
		return nil
	}
	return &BuilderJob{ID: id, Slug: getenv(CloudSlugEnvVar), ProvidersDir: getenv(executables.OverrideEnvVar)}
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
	return nil
}
