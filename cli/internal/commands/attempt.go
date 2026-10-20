package commands

import (
	"context"
	"os"

	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func (p ProviderRun) NewAttempt(ctx context.Context, kind consolev1.DeploymentKind, cfg *project.Project, env *environmentv1.Environment, prNumber string) *deployreport.Attempt {
	trigger, ci := deployreport.ReadTrigger(os.Getenv, prNumber)
	return &deployreport.Attempt{
		Kind:        kind,
		Project:     cfg,
		Provider:    p.providerName(cfg),
		Environment: env,
		Target:      deployreport.ReadTarget(ctx, p.Provider),
		TraceID:     p.Run.TraceID(),
		StartedAt:   p.Run.StartedAt(),
		Trigger:     trigger,
		CI:          ci,
		Source:      p.readSource(cfg.Dir),
	}
}

func (p ProviderRun) readSource(dir string) *consolev1.Source {
	if p.job != nil {
		return p.job.Source()
	}
	return deployreport.ReadSource(dir, os.Getenv)
}

func (p ProviderRun) providerName(cfg *project.Project) string {
	if p.cloud != nil {
		return CloudProviderID
	}
	return providerID(cfg)
}
