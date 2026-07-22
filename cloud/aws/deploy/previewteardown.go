// Preview teardown (ADR 0001): the per-pointer counterpart to DestroyProject.
// `ocel preview rm` removes one preview pointer outright — every app-deploy
// stack the pointer's builds live in, the pointer and its records in the
// preview store, and the pointer's R2/S3 assets — and, for a persistent
// preview, its per-name infra stack (db/bucket) too. Ephemeral previews have no
// infra stack, so there is nothing stateful to remove.
//
// PreviewInfraStackFor is pure and unit-tested directly; RemovePreview drives
// the real store/Pulumi/S3 calls and, like DestroyProject, is exercised only by
// an opt-in run against a live account.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ocelhq/ocel/cloud/edge"
)

// PreviewInfraStackFor is the per-name infra stack a preview pointer owns, or ""
// when it owns none: a persistent preview gets its own isolated db/bucket under
// PreviewInfraStackName, an ephemeral one gets no infra stack at all (its
// functions carry no resource connections). Pure — the single seam the
// ephemeral-vs-persistent teardown branch turns on.
func PreviewInfraStackFor(projectID, pointer string, persistent bool) string {
	if !persistent {
		return ""
	}
	return PreviewInfraStackName(projectID, pointer)
}

// RemovePreview tears one preview pointer down, traffic-first: the store pointer
// goes first (so it stops resolving and the removed record keys name exactly the
// app-deploy stacks and R2 assets to reclaim), then every one of those
// app-deploy stacks, then — for a persistent preview only — the per-name infra
// stack (deleting its db/bucket outright), then the pointer's R2/S3 assets. It
// retains nothing. Best-effort: a failed step never stops the rest, and every
// failure is joined so the host can report what remains and a re-run can resume.
//
// stack/state may be zero when the project never reconciled a preview root stack
// (nothing was ever deployed under this pointer), in which case there is nothing
// store-side to remove and only a stray infra/app stack, if any, is swept.
func RemovePreview(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, projectID, pointer string, persistent bool, progress, log func(string)) error {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	var errs []error
	var removedRecordKeys []string
	if stack != nil && len(state) > 0 {
		report(fmt.Sprintf("Removing preview pointer %q from the store", pointer))
		result, err := stack.RemovePointer(ctx, state, pointer)
		if err != nil {
			errs = append(errs, fmt.Errorf("remove preview pointer %q: %w", pointer, err))
		} else {
			removedRecordKeys = result.RemovedRecordKeys
		}
	}

	targets, err := PreviewReclaimTargets(projectID, pointer, cfg.Env, removedRecordKeys)
	if err != nil {
		errs = append(errs, err)
	} else if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		errs = append(errs, err)
	}

	if infra := PreviewInfraStackFor(projectID, pointer, persistent); infra != "" {
		report("Destroying preview infra stack " + infra + " (database, bucket)")
		if err := Destroy(ctx, teardownConfig(cfg, infra), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview infra stack %s: %w", infra, err))
		}
	}

	return errors.Join(errs...)
}

// PreviewProjectTeardownPlan is what classifyPreviewStacks resolves from the
// preview backend's full stack list: every per-name infra stack (persistent
// previews), every app-deploy stack (all pointers), and the distinct pointers
// they belong to. It is the whole-project preview blast radius `ocel destroy
// --preview` acts on.
type PreviewProjectTeardownPlan struct {
	InfraStacks []string
	AppStacks   []string
	Pointers    []string
}

// previewStackNameInfix is the fixed segment every stacked preview stack carries
// after the project prefix (PreviewInfraStackName/PreviewAppDeployStackName),
// which keeps preview stacks distinct from production's in a shared backend and
// lets a project's previews be told apart from its production stacks by name.
const previewStackNameInfix = "--preview-"

// classifyPreviewStacks splits the preview backend's stack names into one
// project's whole-preview teardown plan. A preview stack is named
// "<safeName(projectID)>--preview-<pointer>--…"; the "…--infra" ones are
// per-name infra stacks and the rest are app-deploy stacks. The pointer segment
// is recovered so the caller can purge each pointer's env-scoped ISR assets.
// Pure.
func classifyPreviewStacks(projectID string, stackNames []string) PreviewProjectTeardownPlan {
	prefix := safeName(projectID) + previewStackNameInfix
	var plan PreviewProjectTeardownPlan
	seenPointer := map[string]struct{}{}
	for _, name := range stackNames {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		pointer, _, ok := strings.Cut(rest, "--")
		if !ok || pointer == "" {
			continue
		}
		if _, dup := seenPointer[pointer]; !dup {
			seenPointer[pointer] = struct{}{}
			plan.Pointers = append(plan.Pointers, pointer)
		}
		if strings.HasSuffix(name, "--infra") {
			plan.InfraStacks = append(plan.InfraStacks, name)
		} else {
			plan.AppStacks = append(plan.AppStacks, name)
		}
	}
	sort.Strings(plan.Pointers)
	return plan
}

// DestroyPreviewProject tears a whole project's preview footprint down against
// the preview substrate, traffic-first: the preview store instance (every
// pointer's history and records in one wipe), then the preview root worker(s),
// then every app-deploy stack and per-name infra stack the preview backend
// holds, then the project's preview R2/S3 assets. It leaves the account-level
// preview bootstrap (the store worker, the CFN preview stack) intact, so a later
// preview deploy still has a substrate to land on. Best-effort throughout; every
// failure is joined so the host can report what remains and a re-run resumes.
//
// The project's preview store slug — which roots the preview generic worker
// names — is read from the persisted root-stack state (RootStackKeySlug), so the
// caller needs no manifest. stack/state may be zero when the project never
// reconciled a preview root stack, in which case only stray stacks/assets are
// swept.
func DestroyPreviewProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, projectID string, progress, log func(string)) error {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	plan, err := planPreviewProjectTeardown(ctx, cfg, projectID)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}

	if stack != nil && len(state) > 0 {
		report("Destroying preview root worker(s)")
		workers, wErr := previewRootWorkerNames(ctx, stack, state, state[edge.RootStackKeySlug], plan.Pointers)
		if wErr != nil {
			errs = append(errs, fmt.Errorf("resolve preview root workers: %w", wErr))
		} else if err := stack.DestroyRootStack(ctx, workers); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview root workers: %w", err))
		}
		// One wipe clears every pointer's history and records at once — the store
		// instance is per-project, not per-pointer. Done after the worker names
		// are read from history so a failure there can re-run.
		report("Wiping the project's preview deployments-store instance")
		if err := stack.DestroyInstance(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview deployments-store instance: %w", err))
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

	report("Purging preview assets")
	if err := purgePreviewAssets(ctx, cfg, projectID, plan.Pointers); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// planPreviewProjectTeardown lists the preview backend's stacks and classifies
// the ones this project owns. It opens a bare Pulumi workspace over the same
// preview backend Destroy selects against.
func planPreviewProjectTeardown(ctx context.Context, cfg Config, projectID string) (PreviewProjectTeardownPlan, error) {
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
	return classifyPreviewStacks(projectID, names), nil
}

// previewRootWorkerNames resolves the preview generic workers a project deployed
// across every pointer. The generic worker name carries no pointer (one worker
// per app fronts all of a project's previews, resolving the pointer per request
// from the subdomain), so the app set is unioned across every pointer's store
// history — the authoritative source of app names, exactly like the production
// rootStackWorkerNames. The shared preview store worker is never in this set: it
// outlives the project and is left for the account-level bootstrap.
func previewRootWorkerNames(ctx context.Context, stack edge.RootStack, state edge.RootStackState, slug string, pointers []string) ([]string, error) {
	apps := map[string]struct{}{}
	for _, pointer := range pointers {
		history, err := stack.History(ctx, state, pointer)
		if err != nil {
			return nil, err
		}
		for _, h := range history {
			for app := range h.Builds {
				apps[app] = struct{}{}
			}
		}
	}

	seen := map[string]struct{}{}
	var names []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	add(previewGenericName(slug, "root"))
	sortedApps := make([]string, 0, len(apps))
	for app := range apps {
		sortedApps = append(sortedApps, app)
	}
	sort.Strings(sortedApps)
	for _, app := range sortedApps {
		add(previewGenericName(slug, app))
	}
	return names, nil
}

// purgePreviewAssets deletes a project's whole preview R2/S3 footprint: its
// static assets (env-agnostic, one project-rooted prefix) and each pointer's
// env-scoped ISR/prerender entries (in both the asset bucket and the cache
// store). Deleting a prefix nothing was written to is a no-op.
func purgePreviewAssets(ctx context.Context, cfg Config, projectID string, pointers []string) error {
	var errs []error
	assets := projectAssetR2Prefix(projectID)
	if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, assets); err != nil {
		errs = append(errs, err)
	}
	for _, pointer := range pointers {
		isr := projectISRPrefix("preview-"+pointer, projectID)
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, isr); err != nil {
			errs = append(errs, err)
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, isr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
