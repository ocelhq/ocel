package deploy

import (
	"context"
	"fmt"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type PhaseError struct {
	Phase string
	After string
}

func (e *PhaseError) Error() string {
	return fmt.Sprintf("deploy phase %q ran before %q handed it anything", e.Phase, e.After)
}

func realize(ctx context.Context, cfg Config, realized *Realized, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) (Result, error) {
	uploadStart := time.Now()
	plan, err := planDeploy(ctx, cfg, manifest, log)
	if err != nil {
		spanForStage(cfg.Tracer, cfg.Stages.Uploading, uploadStart, time.Now(), err)
		return Result{}, err
	}
	up, err := uploadArtifacts(ctx, cfg, manifest, plan, progress)
	spanForStage(cfg.Tracer, cfg.Stages.Uploading, uploadStart, time.Now(), err)
	if err != nil {
		return Result{}, err
	}

	provisionStart := time.Now()
	prov, err := provisionDeploy(ctx, cfg, realized, manifest, plan, up, progress, log)
	spanForStage(cfg.Tracer, cfg.Stages.Provisioning, provisionStart, time.Now(), err)
	if err != nil {
		return Result{StackState: prov.state}, err
	}

	return promoteDeploy(ctx, cfg, manifest, prov, progress)
}
