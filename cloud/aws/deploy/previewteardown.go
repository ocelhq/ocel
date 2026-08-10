// Preview teardown (ADR 0001): the per-pointer counterpart to DestroyProject.
// `ocel preview rm` removes exactly one pointer and never anything the project's
// previews share. An ephemeral preview has no infra stack, so it has nothing
// stateful to remove. RemovePreview drives the real store/Pulumi/S3 calls and,
// like DestroyProject, is exercised end-to-end only by an opt-in run against a
// live account; everything pure here is unit-tested directly.
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
// reports name exactly the app-deploy stacks and R2 assets left to reclaim. A
// persistent preview's own infra stack goes with it; an ephemeral one has none.
// Best-effort: a failed step never stops the rest, and every failure is joined so
// the host can report what remains and a re-run can resume.
//
// It removes that one pointer and NOTHING the project's previews share. The
// entrypoint worker attached to the project's wildcard, the deployments-store
// instance behind it and the project-rooted function artifacts all outlive every
// pointer, and reclaiming them is `ocel destroy --preview`'s job alone. Doing it
// here — on the removal that happened to leave no pointers behind — would let one
// teardown delete the worker out from under a preview another deploy is landing
// at that moment, since a pointer only exists once its deploy has promoted.
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
// The preview entrypoint worker is named deterministically from the slug in the
// persisted root-stack state (previewWorkerName), not read from the store's
// promotion history: one worker fronts every pointer and outlives them, so a
// prior `ocel preview rm` can leave it standing with no history left to name it.
// The edge is enumerated under that same name as a stem so the whole worker
// family goes with it (previewProjectWorkers) — a project deployed under an
// earlier shape of this holds a worker per app, which nothing computes a name
// for any more and which nothing else would ever reclaim.
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

// previewProjectWorkers is the exact set of edge workers a project's preview
// teardown reclaims: its entrypoint worker, always, plus every worker the edge
// reported under that name as a stem — the per-app preview workers an earlier
// shape of this deploy left standing. It filters the reported names itself
// rather than trusting the enumeration, because what comes back is deleted:
// nothing outside the family previewWorkerName heads can get in, so a sibling
// project's worker — which cannot render a name under this project's boundary
// (projectWorkerStem) — and this project's production workers are never among
// them. Sorted, so a re-run tears down in the same order. Pure.
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
// static assets, image configs and edge bundles (env-agnostic, one
// project-rooted prefix each), each pointer's env-scoped ISR/prerender entries
// (in both the asset bucket and the cache store), and its function artifacts
// from the preview substrate's own artifact bucket. Deleting a prefix nothing
// was written to is a no-op.
func purgePreviewAssets(ctx context.Context, cfg Config, slug string, pointers []string) error {
	errs := []error{purgeProjectArtifacts(ctx, cfg, slug)}
	for _, prefix := range []string{projectAssetR2Prefix(slug), projectEdgeR2Prefix(slug)} {
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	// The static assets' other half, plus the image configs that only ever land
	// here: uploadStaticAssets publishes both into the account's own bucket.
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
