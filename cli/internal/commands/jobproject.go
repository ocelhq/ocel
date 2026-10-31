package commands

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const CloudProviderID = "ocel"

func loadJobProject(ctx context.Context, job *jobcontext.Job, cwd, explicitPath string) (*project.Project, error) {
	configPath, err := project.FindConfig(cwd, explicitPath)
	if err != nil {
		return nil, err
	}
	if configPath != "" {
		cfg, err := project.Load(ctx, cwd, explicitPath)
		if err != nil {
			return nil, err
		}
		return adoptJobProject(job, cfg)
	}
	dir, err := project.FindRoot(cwd, explicitPath)
	if err != nil {
		return nil, err
	}
	return adoptJobProject(job, project.NewUnconfigured(dir, job.Slug))
}

func adoptJobProject(job *jobcontext.Job, cfg *project.Project) (*project.Project, error) {
	if cfg.Slug != job.Slug {
		return nil, &clierror.Error{Code: clierror.CodeProjectInvalidConfig, Hint: "slug",
			Cause: fmt.Errorf("job %s was started for the project %s, and %s names %s", job.ID, job.Slug, filepath.Base(cfg.Path), cfg.Slug)}
	}
	if !cfg.HasProvider() {
		cfg.Provider = &project.Provider{ID: CloudProviderID}
	}
	return cfg, nil
}

func (i Invocation) isJobOnCloud(cfg *project.Project) bool {
	return i.Job() != nil && cfg.Provider != nil && cfg.Provider.ID == CloudProviderID
}

func (i Invocation) readCloudWebIdentity(ctx context.Context) (*contractv1.WebIdentity, error) {
	job := i.Job()
	offer, err := console.New(i.JobContext.ConsoleURL).ReadCloudOffer(ctx)
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return nil, &clierror.Error{Code: clierror.CodeCloudUnsupported, Cause: fmt.Errorf("job %s deploys a project that names no provider, and %s does not offer Ocel Cloud", job.ID, i.JobContext.ConsoleURL)}
	}
	token, isJob := i.JobContext.JobToken()
	if !isJob {
		return nil, fmt.Errorf("job %s holds no job token to open Ocel Cloud with", job.ID)
	}
	subject, err := token.ReadSubject(ctx)
	if err != nil {
		return nil, err
	}
	return &contractv1.WebIdentity{TokenFile: token.File, IdentityProvider: offer.APIURL, Principal: subject}, nil
}
