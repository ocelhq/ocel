package commands

import (
	"context"
	"os"

	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func (p ProviderRun) NewAttempt(ctx context.Context, parent *run.Span, kind consolev1.DeploymentKind, cfg *project.Project, env *environmentv1.Environment, prNumber string) *deployreport.Attempt {
	ctx, reading := parent.Child(ctx, cfg.Slug, progress.Reading.Title("the target and the source checkout"))
	defer reading.End(nil)
	trigger, ci := p.readTrigger(prNumber)
	target, err := deployreport.ReadTarget(ctx, p.Provider)
	if err != nil {
		reading.Warn(err.Error())
	}
	return &deployreport.Attempt{
		Kind:        kind,
		Project:     cfg,
		Provider:    p.providerName(cfg),
		Environment: env,
		Target:      target,
		TraceID:     p.deploymentID(),
		StartedAt:   p.Run.StartedAt(),
		Trigger:     trigger,
		CI:          ci,
		Source:      p.readSource(ctx, cfg.Dir),
	}
}

func (p ProviderRun) readTrigger(prNumber string) (*consolev1.Trigger, *consolev1.CI) {
	if p.job != nil {
		return deployreport.ReadRunnerTrigger(prNumber)
	}
	return deployreport.ReadTrigger(os.Getenv, prNumber)
}

func (p ProviderRun) deploymentID() string {
	if p.job != nil && p.job.DeploymentID != "" {
		return p.job.DeploymentID
	}
	return p.Run.TraceID()
}

func (p ProviderRun) readSource(ctx context.Context, dir string) *consolev1.Source {
	if p.job != nil {
		return p.job.Source()
	}
	return deployreport.ReadSource(ctx, dir, os.Getenv)
}

func (p ProviderRun) providerName(cfg *project.Project) string {
	if p.cloud != nil {
		return CloudProviderID
	}
	return providerID(cfg)
}
