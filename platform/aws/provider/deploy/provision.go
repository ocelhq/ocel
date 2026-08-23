package deploy

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type stackInputs struct {
	transformed *transformedArgs
	sessions    sessionScope
	artifacts   map[string]artifactRef
	layer       payloads.Placement
	completer   payloads.Placement
	realized    *Realized
	release     *Releaser
}

type provisionedDeploy struct {
	ready       bool
	stack       edge.EdgeStack
	state       edge.StackState
	promotionID string
	links       []*linksv1.Link
	results     []appDeployResult
	outputs     [][]*progressv1.FunctionOutput
}

func provisionDeploy(ctx context.Context, cfg Config, realized *Realized, manifest *contractv1.Manifest, plan deployPlan, up uploadedArtifacts, progress Progress, log func(string)) (provisionedDeploy, error) {
	if !plan.ready {
		return provisionedDeploy{}, &PhaseError{Phase: "provision", After: "plan"}
	}
	if !up.ready {
		return provisionedDeploy{}, &PhaseError{Phase: "provision", After: "upload"}
	}

	identities := plan.builds.identities
	promotionID, err := newRandomID()
	if err != nil {
		return provisionedDeploy{}, err
	}
	stacks, err := BuildPlan(manifest, planEnvironment(cfg), promotionID, identities)
	if err != nil {
		return provisionedDeploy{}, err
	}

	transformed, err := resolveTransforms(ctx, cfg, manifest)
	if err != nil {
		return provisionedDeploy{}, err
	}
	in := stackInputs{
		transformed: transformed,
		sessions:    plan.sessions,
		artifacts:   up.artifacts,
		layer:       up.layer,
		completer:   up.completer,
		realized:    realized,
		release:     NewReleaser(cfg, realized),
	}

	index, err := stackIndex(cfg.Stacks)
	if err != nil {
		return provisionedDeploy{}, err
	}
	if err := index.AddProject(ctx, naming.Sanitize(manifest.GetSlug()), cfg.RequiredFeatures); err != nil {
		return provisionedDeploy{}, err
	}

	progress.report(progressv1.Phase_PHASE_PROVISIONING, "Reconciling the edge stack", 0, 0)
	specs, err := stackSpecs(cfg, manifest, stackVersion, log)
	if err != nil {
		return provisionedDeploy{}, err
	}
	edgeStack, err := reconcileStack(ctx, cfg.Edge, specs, MarkGlobalPreview(cfg.StackState, cfg, manifest))
	if err != nil {
		return provisionedDeploy{state: reconciledState(edgeStack, cfg)}, err
	}
	state := MarkGlobalPreview(edgeStack.State(), cfg, manifest)
	state, err = settleStackRecords(ctx, cfg, specs, state, log)
	if err != nil {
		return provisionedDeploy{state: state}, err
	}

	var links []*linksv1.Link
	if !stacks.InfraStack.IsZero() {
		progress.report(progressv1.Phase_PHASE_PROVISIONING, "Provisioning infra stack", 0, 0)
		links, err = runInfraStack(ctx, cfg, in, manifest, stacks, log)
		if err != nil {
			return provisionedDeploy{state: state}, err
		}
	}
	if err := checkLinkGrants(links); err != nil {
		return provisionedDeploy{state: state}, err
	}
	if err := publishLinkRecords(ctx, cfg, manifest, links); err != nil {
		return provisionedDeploy{state: state}, err
	}
	reportGrantVersions(plan.consumed, log)
	granting := append(slices.Clone(links), consumedLinks(plan.consumed)...)

	progress.report(progressv1.Phase_PHASE_PROVISIONING, "Provisioning app-deploy stacks", 0, 0)
	apps := plan.apps
	results := make([]appDeployResult, len(apps))
	appOutputs := make([][]*progressv1.FunctionOutput, len(apps))
	appFunctionNames := make([]map[string]string, len(apps))
	runAppStacks(apps, func(i int, app *contractv1.ManifestApp) {
		id := identities[app.GetName()]
		outs, names, err := runAppStack(ctx, cfg, in, manifest, stacks, app, id, plan.baked[app.GetName()], plan.builds, granting, cfg.AppStages[app.GetName()], log)
		appOutputs[i] = outs
		appFunctionNames[i] = names
		record, recErr := buildDeploymentRecord(cfg, plan.needs, manifest, app, id, outs, plan.builds, names)
		if err == nil {
			err = recErr
		}
		results[i] = appDeployResult{App: app.GetName(), Identity: id, Record: record, Err: err}
	})

	warmed := warmDeployedFunctions(ctx, cfg, manifest, appFunctionNames, plan.builds, log)
	embedBytecodeCaches(ctx, cfg, manifest, up.artifacts, warmed, plan.builds, log)

	return provisionedDeploy{
		ready:       true,
		stack:       edgeStack,
		state:       state,
		promotionID: promotionID,
		links:       links,
		results:     results,
		outputs:     appOutputs,
	}, nil
}
