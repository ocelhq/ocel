package commands

import (
	"context"
	"os"
	"time"

	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func (p ProviderRun) BeginAttempt(ctx context.Context, kind consolev1.DeploymentKind, cfg *project.Project, env *environmentv1.Environment, prNumber string) deployreport.Attempt {
	trigger, ci := deployreport.ReadTrigger(os.Getenv, prNumber)
	return deployreport.Attempt{
		Kind:        kind,
		Project:     cfg,
		Environment: env,
		Target:      deployreport.ReadTarget(ctx, p.Provider),
		TraceID:     p.Run.TraceID(),
		StartedAt:   p.Run.StartedAt(),
		Trigger:     trigger,
		CI:          ci,
		Source:      deployreport.ReadSource(cfg.Dir, os.Getenv),
	}
}

func (p ProviderRun) NewEnvironmentEvent(kind consolev1.EnvironmentEventKind, env *environmentv1.Environment) *consolev1.EnvironmentEvent {
	return NewEnvironmentEvent(p.Run, p.Project.Dir, kind, env)
}

func NewEnvironmentEvent(running *run.Run, projectDir string, kind consolev1.EnvironmentEventKind, env *environmentv1.Environment) *consolev1.EnvironmentEvent {
	_, ci := deployreport.ReadTrigger(os.Getenv, "")
	return deployreport.NewEnvironmentEvent(kind, env, running.TraceID(), time.Now(), ci, deployreport.ReadSource(projectDir, os.Getenv))
}
