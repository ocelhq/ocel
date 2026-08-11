package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func PreviewInfraStackFor(pointer string, persistent bool) naming.StackName {
	if !persistent {
		return naming.StackName{}
	}
	return naming.InfraStack(pointer)
}

func RemovePreview(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug, pointer string, persistent bool, progress, log func(string)) error {
	report := nilSafe(progress)

	var errs []error
	var removal edge.PruneResult
	if stack != nil && len(state) > 0 {
		report(fmt.Sprintf("Removing preview pointer %q from the store", pointer))
		result, err := stack.RemovePointer(ctx, state, pointer)
		if err != nil {
			errs = append(errs, fmt.Errorf("remove preview pointer %q: %w", pointer, err))
		} else {
			removal = result
		}
	}

	targets, err := ReclaimTargets(slug, pointer, removal.RemovedRecordKeys, removal.SurvivingRecordKeys, removal.SurvivingPointerRecordKeys)
	if err != nil {
		errs = append(errs, err)
	} else if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		errs = append(errs, err)
	}

	if infra := PreviewInfraStackFor(pointer, persistent); !infra.IsZero() {
		report("Destroying preview infra stack " + infra.String() + " (database, bucket)")
		if err := Destroy(ctx, teardownConfig(cfg, infra), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview infra stack %s: %w", infra, err))
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

func DestroyPreviewProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, progress, log func(string)) (DestroyProjectResult, error) {
	progress, log = serializeReports(progress, log)
	report := nilSafe(progress)

	plan, err := planPreviewProjectTeardown(ctx, cfg, slug)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	result := DestroyProjectResult{RootTornDown: true}

	if stack != nil && len(state) > 0 {
		report("Destroying the preview root workers")
		stateSlug := state[edge.RootStackKeySlug]
		var deployed []string
		for _, stem := range previewWorkerStems(stateSlug) {
			under, err := stack.ListDeployedWorkers(ctx, stem)
			if err != nil {
				errs = append(errs, fmt.Errorf("list preview root workers under %q: %w", stem, err))
				result.RootTornDown = false
				continue
			}
			deployed = append(deployed, under...)
		}
		if err := stack.DestroyRootStack(ctx, previewProjectWorkers(stateSlug, deployed)); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview root workers: %w", err))
			result.RootTornDown = false
		}
		report("Wiping the project's preview deployments-store instance")
		if err := stack.DestroyInstance(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview deployments-store instance: %w", err))
			result.RootTornDown = false
		}
	}

	errs = append(errs, destroyPhased(plan.AppStacks, plan.InfraStacks,
		func(stack naming.StackName) error {
			report("Destroying preview app stack " + stack.String())
			if err := Destroy(ctx, teardownConfig(cfg, stack), progress, log); err != nil {
				return fmt.Errorf("destroy preview app stack %s: %w", stack, err)
			}
			return nil
		},
		func(stack naming.StackName) error {
			report("Destroying preview infra stack " + stack.String() + " (database, bucket)")
			if err := Destroy(ctx, teardownConfig(cfg, stack), progress, log); err != nil {
				return fmt.Errorf("destroy preview infra stack %s: %w", stack, err)
			}
			return nil
		})...)

	if err := purgeProjectValues(ctx, cfg, slug, report); err != nil {
		errs = append(errs, err)
	}

	report("Purging preview assets")
	if err := purgePreviewAssets(ctx, cfg, slug, plan.Pointers); err != nil {
		errs = append(errs, err)
	}

	if err := forgetProjectIfEmpty(ctx, cfg.Stacks, slug); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

func previewWorkerStems(slug string) []string {
	if slug == "" {
		return nil
	}
	return []string{previewWorkerStem(slug), retiredPreviewWorkerStem(slug)}
}

func previewProjectWorkers(slug string, deployed []string) []string {
	stems := previewWorkerStems(slug)
	if len(stems) == 0 {
		return nil
	}
	names := []string{previewWorkerName(slug), retiredPreviewWorkerStem(slug)}
	for _, name := range deployed {
		for _, stem := range stems {
			if edge.NameUnderStem(stem, name) && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

func planPreviewProjectTeardown(ctx context.Context, cfg Config, slug string) (PreviewProjectTeardownPlan, error) {
	stacks, err := indexedStacks(ctx, cfg.Stacks, slug)
	if err != nil {
		return PreviewProjectTeardownPlan{}, err
	}
	return classifyPreviewStacks(stacks), nil
}

func purgePreviewAssets(ctx context.Context, cfg Config, slug string, pointers []string) error {
	errs := []error{purgeProjectArtifacts(ctx, cfg, slug)}
	for _, prefix := range []string{projectAssetR2Prefix(slug), projectEdgeR2Prefix(slug)} {
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	for _, prefix := range []string{projectAssetR2Prefix(slug), projectImageConfigPrefix(slug)} {
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	for _, pointer := range pointers {
		isr := projectISRPrefix(pointer, slug)
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, isr); err != nil {
			errs = append(errs, err)
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, isr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
