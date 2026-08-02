// Preview teardown (ADR 0001): the per-pointer counterpart to DestroyProject.
// `ocel preview rm` retains nothing. An ephemeral preview has no infra stack, so
// it has nothing stateful to remove; removing the project's last preview also
// reclaims the generic worker(s) and store instance every pointer shared, which
// nothing else ever would. RemovePreview drives the real store/Pulumi/S3 calls
// and, like DestroyProject, is exercised end-to-end only by an opt-in run against
// a live account; everything pure here is unit-tested directly.
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
func PreviewInfraStackFor(slug, pointer string, persistent bool) string {
	if !persistent {
		return ""
	}
	return PreviewInfraStackName(slug, pointer)
}

// RemovePreview tears one preview pointer down, traffic-first: the store pointer
// goes first both so it stops resolving and because the record keys its removal
// reports name exactly the app-deploy stacks and R2 assets left to reclaim.
// Best-effort: a failed step never stops the rest, and every failure is joined so
// the host can report what remains and a re-run can resume — which is why the edge
// sweep goes last, leaving the state and instance a re-run needs in place if an
// earlier step failed.
//
// stack/state may be zero when the project never reconciled a preview root stack
// (nothing was ever deployed under this pointer), in which case there is nothing
// store-side to remove and only a stray infra/app stack, if any, is swept.
func RemovePreview(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug, pointer string, persistent bool, progress, log func(string)) error {
	report := nilSafe(progress)

	var errs []error
	var removal edge.PointerRemoval
	removed := false
	if stack != nil && len(state) > 0 {
		report(fmt.Sprintf("Removing preview pointer %q from the store", pointer))
		result, err := stack.RemovePointer(ctx, state, pointer)
		if err != nil {
			errs = append(errs, fmt.Errorf("remove preview pointer %q: %w", pointer, err))
		} else {
			removal, removed = result, true
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

	if removed {
		errs = append(errs, reclaimPreviewEdge(ctx, stack, state, slug, removal, report))

		// Artifact keys carry no pointer, so identical code under two pointers is
		// one object: only the last pointer's removal leaves a prefix no live
		// preview still points at. The `removed` guard above is load-bearing —
		// RemainingPointers is also zero when RemovePointer failed, and purging on
		// that would take every sibling pointer's artifacts with it.
		if removal.RemainingPointers == 0 {
			report("Purging preview function artifacts — no previews remain")
			errs = append(errs, purgeProjectArtifacts(ctx, cfg, slug))
		}
	}

	return errors.Join(errs...)
}

// reclaimPreviewEdge frees the edge footprint a removed preview pointer leaves:
// just this pointer's routes off the generic worker its live siblings share, or —
// once it was the last pointer — those whole workers (each taking its routes with
// it) and the project's store instance. Best-effort like its caller.
func reclaimPreviewEdge(ctx context.Context, stack edge.RootStack, state edge.RootStackState, slug string, removal edge.PointerRemoval, report func(string)) error {
	if removal.RemainingPointers > 0 {
		var errs []error
		for _, route := range removal.RemovedRoutes {
			worker := previewGenericName(slug, route.App)
			report("Removing preview route " + route.Hostname)
			if err := stack.RemoveRoute(ctx, worker, route.Hostname); err != nil {
				errs = append(errs, fmt.Errorf("remove preview route %s from %s: %w", route.Hostname, worker, err))
			}
		}
		return errors.Join(errs...)
	}

	var errs []error
	report("Destroying the project's preview worker(s) — no previews remain")
	deployed, listErr := stack.ListDeployedWorkers(ctx, previewWorkerPrefix(slug))
	if listErr != nil {
		errs = append(errs, fmt.Errorf("resolve preview root workers: %w", listErr))
	}
	destroyErr := stack.DestroyRootStack(ctx, previewSweepWorkers(slug, removal, deployed))
	if destroyErr != nil {
		errs = append(errs, fmt.Errorf("destroy preview root workers: %w", destroyErr))
	}

	// The instance is the only thing a re-run can name these workers from: wiping
	// it while any of them may still stand leaves them unreclaimable, because a
	// second `preview rm` finds no pointer to remove and never reaches here.
	if listErr == nil && destroyErr == nil {
		report("Wiping the project's preview deployments-store instance")
		if err := stack.DestroyInstance(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview deployments-store instance: %w", err))
		}
	}
	return errors.Join(errs...)
}

// previewSweepWorkers is the set of preview generic workers a last-pointer
// teardown destroys: every worker the edge reports under the project's preview
// prefix, plus the deployed name of every app the removal names. Both sources are
// needed — the prefix reaches workers whose app nothing remembers any more, while
// the removal reaches names workerScriptName clamped out of that prefix to fit the
// platform's name limit (a long slug). Destroying a name already gone is not an
// error, so the union only ever risks a redundant call, where a missed name leaks
// a billable worker. Pure.
func previewSweepWorkers(slug string, removal edge.PointerRemoval, deployed []string) []string {
	workers := make([]string, 0, len(deployed)+len(removal.RemovedRoutes))
	seen := map[string]struct{}{}
	add := func(name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		workers = append(workers, name)
	}
	for _, name := range deployed {
		add(name)
	}
	for _, route := range removal.RemovedRoutes {
		if route.App != "" {
			add(previewGenericName(slug, route.App))
		}
	}
	for _, key := range removal.RemovedRecordKeys {
		if app, _, ok := splitRecordKey(key); ok {
			add(previewGenericName(slug, app))
		}
	}
	return workers
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

// previewStackPointer recovers a preview stack's pointer from its name, or
// reports ok=false for anything that isn't a preview of slug (production
// stacks, another project's previews, a sibling whose id merely prefixes ours,
// the retired single-stack shape). A preview stack is named
// "<safeName(slug)>--preview-<pointer>--…"; the "…--infra" tail marks the
// pointer's per-name infra stack. This is the single shared parse `ocel preview
// ls` and preview teardown both build on, so their view of the naming can never
// drift. Pure.
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

// classifyPreviewStacks splits the preview backend's stack names into one
// project's whole-preview teardown plan: per-name infra stacks, app-deploy
// stacks, and the distinct pointers they belong to (recovered so the caller can
// purge each pointer's env-scoped ISR assets). Pure.
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

// DestroyPreviewProject tears a whole project's preview footprint down against
// the preview substrate, traffic-first: the preview store instance (every
// pointer's history and records in one wipe), then the preview root worker(s),
// then every app-deploy stack and per-name infra stack the preview backend
// holds, then the project's preview R2/S3 assets. It leaves the account-level
// preview bootstrap (the store worker, the CFN preview stack) intact, so a later
// preview deploy still has a substrate to land on. Best-effort throughout; every
// failure is joined so the host can report what remains and a re-run resumes.
//
// The preview generic workers are found by listing the edge for every worker
// under the project's preview name prefix (previewWorkerPrefix, rooted at the
// slug in the persisted root-stack state), not from the store's promotion
// history: a shared generic worker fronts every pointer and outlives them, so a
// prior `ocel preview rm` can leave it standing with no history left to name it.
// stack/state may be zero when the project never reconciled a preview root
// stack, in which case only stray stacks/assets are swept.
//
// RootTornDown reports that nothing edge-side is left, exactly as DestroyProject
// does: it is the caller's signal that the persisted state may be forgotten. A
// stack or asset failure below leaves it true — those hold no identity a re-run
// needs, and gating the state on them would strand a project whose store
// instance and workers are already gone.
func DestroyPreviewProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, progress, log func(string)) (DestroyProjectResult, error) {
	report := nilSafe(progress)

	plan, err := planPreviewProjectTeardown(ctx, cfg, slug)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	result := DestroyProjectResult{RootTornDown: true}

	if stack != nil && len(state) > 0 {
		report("Destroying preview root worker(s)")
		workers, wErr := stack.ListDeployedWorkers(ctx, previewWorkerPrefix(state[edge.RootStackKeySlug]))
		if wErr != nil {
			errs = append(errs, fmt.Errorf("resolve preview root workers: %w", wErr))
			result.RootTornDown = false
		} else if err := stack.DestroyRootStack(ctx, workers); err != nil {
			errs = append(errs, fmt.Errorf("destroy preview root workers: %w", err))
			result.RootTornDown = false
		}
		// One wipe clears every pointer's history and records at once — the store
		// instance is per-project, not per-pointer. Done after the worker names
		// are read from history so a failure there can re-run.
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

	// Every pointer of the project is going, so the whole preview class's
	// partition goes with them — class-wide preview values and every named
	// environment's override alike. Removing one preview (RemovePreview) never
	// reaches here: that keeps its overrides for the redeploy of the same branch.
	if err := purgeProjectValues(ctx, cfg, slug, report); err != nil {
		errs = append(errs, err)
	}

	report("Purging preview assets")
	if err := purgePreviewAssets(ctx, cfg, slug, plan.Pointers); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

// planPreviewProjectTeardown lists the preview backend's stacks and classifies
// the ones this project owns. It opens a bare Pulumi workspace over the same
// preview backend Destroy selects against.
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

// purgePreviewAssets deletes a project's whole preview R2/S3 footprint: its
// static assets and edge bundles (env-agnostic, one project-rooted prefix
// each), each pointer's env-scoped ISR/prerender entries (in both the asset
// bucket and the cache store), and its function artifacts from the preview
// substrate's own artifact bucket. Deleting a prefix nothing was written to is
// a no-op.
func purgePreviewAssets(ctx context.Context, cfg Config, slug string, pointers []string) error {
	errs := []error{purgeProjectArtifacts(ctx, cfg, slug)}
	for _, prefix := range []string{projectAssetR2Prefix(slug), projectEdgeR2Prefix(slug)} {
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, prefix); err != nil {
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
