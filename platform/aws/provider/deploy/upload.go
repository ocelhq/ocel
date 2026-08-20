package deploy

import (
	"context"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type uploadedArtifacts struct {
	ready     bool
	artifacts map[string]artifactRef
	layer     payloads.Placement
	completer payloads.Placement
}

func uploadArtifacts(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan deployPlan, progress Progress) (uploadedArtifacts, error) {
	if !plan.ready {
		return uploadedArtifacts{}, &PhaseError{Phase: "upload", After: "plan"}
	}

	artifacts, err := uploadFunctionArtifacts(ctx, cfg, manifest, plan.builds, progress)
	if err != nil {
		return uploadedArtifacts{}, err
	}
	up := uploadedArtifacts{ready: true, artifacts: artifacts}

	if len(manifest.GetFunctions()) > 0 {
		if up.layer, err = placeMembraneLayer(ctx, cfg); err != nil {
			return uploadedArtifacts{}, err
		}
	}
	if completesUploads(manifest) {
		if up.completer, err = placeUploadCompleter(ctx, cfg); err != nil {
			return uploadedArtifacts{}, err
		}
	}

	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading prerender assets", 0, 0)
	if err := uploadPrerenderAssets(ctx, cfg, plan.builds); err != nil {
		return uploadedArtifacts{}, err
	}
	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading static assets", 0, 0)
	if err := uploadStaticAssets(ctx, cfg, manifest, plan.builds); err != nil {
		return uploadedArtifacts{}, err
	}
	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading edge bundles", 0, 0)
	if err := uploadEdgeBundles(ctx, cfg, manifest, plan.builds); err != nil {
		return uploadedArtifacts{}, err
	}
	return up, nil
}
