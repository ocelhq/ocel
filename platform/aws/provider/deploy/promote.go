package deploy

import (
	"context"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func promoteDeploy(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, prov provisionedDeploy, progress Progress) (Result, error) {
	if !prov.ready {
		return Result{StackState: prov.state}, &PhaseError{Phase: "promote", After: "provision"}
	}

	finalizeStart := time.Now()
	progress.report(deploymentsv1.Phase_PHASE_FINALIZING, "Staging and promoting", 0, 0)
	promoted, err := stageAndPromote(ctx, cfg, prov.stack, prov.promotionID, cfg.Tag, promotePointer(cfg), time.Now().Unix(), prov.results)
	spanForStage(cfg.Tracer, cfg.Stages.Finalizing, finalizeStart, time.Now(), err)
	if err != nil {
		return Result{StackState: prov.state}, err
	}

	var functions []*deploymentsv1.FunctionOutput
	for _, outs := range prov.outputs {
		functions = append(functions, outs...)
	}
	functions = append(functions, workerURLOutputs(cfg, manifest)...)
	return Result{
		Links:       prov.links,
		Functions:   functions,
		AppURLs:     appURLs(manifest, functions),
		PromotionID: prov.promotionID,
		Flip:        promoted.Flip,
		StackState:  prov.state,
	}, nil
}
