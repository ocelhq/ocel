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
		Environment: env,
		Target:      deployreport.ReadTarget(ctx, p.Provider),
		TraceID:     p.Run.TraceID(),
		StartedAt:   p.Run.StartedAt(),
		Trigger:     trigger,
		CI:          ci,
		Source:      deployreport.ReadSource(cfg.Dir, os.Getenv),
	}
}
