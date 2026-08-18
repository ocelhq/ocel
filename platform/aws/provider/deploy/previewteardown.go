package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func PreviewInfraStackFor(pointer string, persistent bool) naming.StackName {
	if !persistent {
		return naming.StackName{}
	}
	return naming.InfraStack(pointer)
}

type PreviewRemovalStages struct {
	Pointer Stage
	Reclaim Stage
	Infra   Stage
}

func (s PreviewRemovalStages) Roots() []Stage {
	roots := []Stage{s.Pointer, s.Reclaim}
	if s.Infra.ID != (StageID{}) {
		roots = append(roots, s.Infra)
	}
	return roots
}

func RemovePreview(ctx context.Context, stack edge.EdgeStack, cfg Config, slug, pointer string, persistent bool, stages PreviewRemovalStages, log func(string)) error {
	var errs []error
	var removal edge.PruneResult

	pointerStart := time.Now()
	var pointerErr error
	if stack != nil && len(stack.State()) > 0 {
		report := cfg.reportStage(stages.Pointer)
		report(sanitizeMessage(fmt.Sprintf("Removing preview pointer %q from the store", pointer)))
		result, err := stack.RemovePointer(ctx, pointer)
		if err != nil {
			pointerErr = fmt.Errorf("remove preview pointer %q: %w", pointer, err)
		} else {
			removal = result
		}
	}
	spanForStage(cfg.Tracer, stages.Pointer, pointerStart, time.Now(), pointerErr)
	if pointerErr != nil {
		errs = append(errs, pointerErr)
	}

	targets, err := ReclaimTargets(slug, pointer, removal.RemovedRecordKeys, removal.SurvivingRecordKeys, removal.SurvivingPointerRecordKeys)
	if err != nil {
		declareStages(cfg.Tracer, true)
		errs = append(errs, err)
	} else if err := Reclaim(ctx, cfg, targets, stages.Reclaim, log); err != nil {
		errs = append(errs, err)
	}

	if infra := PreviewInfraStackFor(pointer, persistent); !infra.IsZero() {
		if err := destroyStackStage(ctx, cfg, infra, stages.Infra, "preview infra", log); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

type PreviewProjectTeardownPlan struct {
	InfraStacks []naming.StackName
	AppStacks   []naming.StackName
	Pointers    []string
}

func classifyPreviewStacks(stacks []naming.StackName) PreviewProjectTeardownPlan {
	var plan PreviewProjectTeardownPlan
	seenPointer := map[string]struct{}{}
	for _, stack := range stacks {
		if stack.Env == ProductionEnv {
			continue
		}
		if _, dup := seenPointer[stack.Env]; !dup {
			seenPointer[stack.Env] = struct{}{}
			plan.Pointers = append(plan.Pointers, stack.Env)
		}
		if stack.IsInfra() {
			plan.InfraStacks = append(plan.InfraStacks, stack)
		} else {
			plan.AppStacks = append(plan.AppStacks, stack)
		}
	}
	slices.Sort(plan.Pointers)
	return plan
}

func DestroyPreviewProject(ctx context.Context, stack edge.EdgeStack, cfg Config, slug string, stages ProjectTeardownStages, log func(string)) (DestroyProjectResult, error) {
	var errs []error
	result := DestroyProjectResult{EdgeTornDown: true}

	planStart := time.Now()
	plan, err := planPreviewProjectTeardown(ctx, cfg, slug)
	if err != nil {
		errs = append(errs, err)
	}
	spanForStage(cfg.Tracer, stages.Planning, planStart, time.Now(), err)

	appChildren, appDeclared := childStagesFor(stages.AppStacks, plan.AppStacks)
	infraChildren, infraDeclared := childStagesFor(stages.InfraStacks, plan.InfraStacks)
	declareStages(cfg.Tracer, true, append(appDeclared, infraDeclared...)...)

	edgeStart := time.Now()
	var edgeErr error
	if stack != nil && len(stack.State()) > 0 {
		report := cfg.reportStage(stages.Edge)
		report("Destroying the preview root workers and the deployments-store instance")
		prior := stack.State()
		if err := stack.Destroy(ctx); err != nil {
			edgeErr = errors.Join(edgeErr, fmt.Errorf("destroy the preview edge stack: %w", err))
			result.EdgeTornDown = false
		} else if err := releaseRecords(ctx, cfg, prior, report); err != nil {
			edgeErr = errors.Join(edgeErr, err)
		}
	}
	spanForStage(cfg.Tracer, stages.Edge, edgeStart, time.Now(), edgeErr)
	if edgeErr != nil {
		errs = append(errs, edgeErr)
	}

	stacksStart := time.Now()
	appErrs, infraErrs := destroyPhased(plan.AppStacks, plan.InfraStacks,
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, cfg, stack, appChildren[stack], "preview app", log)
		},
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, cfg, stack, infraChildren[stack], "preview infra", log)
		})
	spanForStage(cfg.Tracer, stages.AppStacks, stacksStart, time.Now(), errors.Join(appErrs...))
	spanForStage(cfg.Tracer, stages.InfraStacks, stacksStart, time.Now(), errors.Join(infraErrs...))
	errs = append(errs, appErrs...)
	errs = append(errs, infraErrs...)

	valuesStart := time.Now()
	verr := purgeProjectValues(ctx, cfg, slug, cfg.reportStage(stages.Values))
	spanForStage(cfg.Tracer, stages.Values, valuesStart, time.Now(), verr)
	if verr != nil {
		errs = append(errs, verr)
	}

	assetsStart := time.Now()
	cfg.reportStage(stages.Assets)("Purging preview assets")
	aerr := purgePreviewAssets(ctx, cfg, slug, previewPurgeEnvs(plan, cfg.Env))
	spanForStage(cfg.Tracer, stages.Assets, assetsStart, time.Now(), aerr)
	if aerr != nil {
		errs = append(errs, aerr)
	}

	forgetStart := time.Now()
	cfg.reportStage(stages.Forget)("Forgetting the project")
	ferr := forgetProjectIfEmpty(ctx, cfg.Stacks, slug)
	spanForStage(cfg.Tracer, stages.Forget, forgetStart, time.Now(), ferr)
	if ferr != nil {
		errs = append(errs, ferr)
	}

	return result, errors.Join(errs...)
}

func planPreviewProjectTeardown(ctx context.Context, cfg Config, slug string) (PreviewProjectTeardownPlan, error) {
	stacks, err := indexedStacks(ctx, cfg.Stacks, slug)
	if err != nil {
		return PreviewProjectTeardownPlan{}, err
	}
	return classifyPreviewStacks(stacks), nil
}

func previewPurgeEnvs(plan PreviewProjectTeardownPlan, env string) []string {
	if env == EveryPreview || env == ProductionEnv {
		return plan.Pointers
	}
	return purgeEnvs(slices.Concat(plan.AppStacks, plan.InfraStacks), env)
}

func purgePreviewAssets(ctx context.Context, cfg Config, slug string, envs []string) error {
	return purgeProjectAssets(ctx, cfg, slug, envs)
}
