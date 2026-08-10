package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ocelhq/ocel/cloud/edge"
)

func PreviewInfraStackFor(slug, pointer string, persistent bool) string {
	if !persistent {
		return ""
	}
	return PreviewInfraStackName(slug, pointer)
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

	targets, err := PreviewReclaimTargets(slug, pointer, cfg.Env, removal.RemovedRecordKeys, removal.SurvivingRecordKeys, removal.SurvivingPointerRecordKeys)
	if err != nil {
		errs = append(errs, err)
	} else if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		errs = append(errs, err)
	}

	if infra := PreviewInfraStackFor(slug, pointer, persistent); infra != "" {
		report("Destroying preview infra stack " + infra + " (database, bucket)")
		if err := Destroy(ctx, teardownConfig(cfg, infra), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview infra stack %s: %w", infra, err))
		}
	}

	return errors.Join(errs...)
}

type PreviewProjectTeardownPlan struct {
	InfraStacks []string
	AppStacks   []string
	Pointers    []string
}

const previewStackNameInfix = "--preview-"

func previewStackPointer(slug, name string) (pointer string, infra, ok bool) {
	prefix := safeName(slug) + previewStackNameInfix
	if !strings.HasPrefix(name, prefix) {
		return "", false, false
	}
	pointer, _, ok = strings.Cut(strings.TrimPrefix(name, prefix), "--")
	if !ok || pointer == "" {
		return "", false, false
	}
	return pointer, strings.HasSuffix(name, "--infra"), true
}

func classifyPreviewStacks(slug string, stackNames []string) PreviewProjectTeardownPlan {
	var plan PreviewProjectTeardownPlan
	seenPointer := map[string]struct{}{}
	for _, name := range stackNames {
		pointer, infra, ok := previewStackPointer(slug, name)
		if !ok {
			continue
		}
		if _, dup := seenPointer[pointer]; !dup {
			seenPointer[pointer] = struct{}{}
			plan.Pointers = append(plan.Pointers, pointer)
		}
		if infra {
			plan.InfraStacks = append(plan.InfraStacks, name)
		} else {
			plan.AppStacks = append(plan.AppStacks, name)
		}
	}
	sort.Strings(plan.Pointers)
	return plan
}

func DestroyPreviewProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, progress, log func(string)) (DestroyProjectResult, error) {
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
		deployed, err := stack.ListDeployedWorkers(ctx, previewWorkerName(stateSlug))
		if err != nil {
			errs = append(errs, fmt.Errorf("list preview root workers: %w", err))
			result.RootTornDown = false
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

	for _, name := range plan.AppStacks {
		report("Destroying preview app stack " + name)
		if err := Destroy(ctx, teardownConfig(cfg, name), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview app stack %s: %w", name, err))
		}
	}
	for _, name := range plan.InfraStacks {
		report("Destroying preview infra stack " + name + " (database, bucket)")
		if err := Destroy(ctx, teardownConfig(cfg, name), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview infra stack %s: %w", name, err))
		}
	}

	if err := purgeProjectValues(ctx, cfg, slug, report); err != nil {
		errs = append(errs, err)
	}

	report("Purging preview assets")
	if err := purgePreviewAssets(ctx, cfg, slug, plan.Pointers); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

func previewProjectWorkers(slug string, deployed []string) []string {
	stem := previewWorkerName(slug)
	if slug == "" || stem == "" {
		return nil
	}
	names := []string{stem}
	for _, name := range deployed {
		if edge.NameUnderStem(stem, name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func planPreviewProjectTeardown(ctx context.Context, cfg Config, slug string) (PreviewProjectTeardownPlan, error) {
	ws, err := backendWorkspace(ctx, cfg.ProjectName, cfg.BackendURL, cfg.Passphrase, cfg.Region, cfg.Pulumi)
	if err != nil {
		return PreviewProjectTeardownPlan{}, err
	}
	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return PreviewProjectTeardownPlan{}, fmt.Errorf("list preview stacks: %w", err)
	}
	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	return classifyPreviewStacks(slug, names), nil
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
		isr := projectISRPrefix("preview-"+pointer, slug)
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, isr); err != nil {
			errs = append(errs, err)
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, isr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
