// Whole-project teardown (ADR 0001): the cross-cutting counterpart to a
// production Run. Where Destroy removes one named stack, DestroyProject removes
// everything a production project owns across both systems — the imperative
// root stack (via the edge), every app-deploy stack and the stateful infra
// stack (Pulumi), and the project's R2/S3 assets. It is best-effort: a failed
// step never stops the rest, and every failure is joined into the returned
// error so the host can report exactly what remains and a re-run can resume.
//
// classifyProjectStacks is pure and unit-tested directly; DestroyProject drives
// the real Pulumi/edge/S3 calls and, like Run/Destroy/Prune, is exercised only
// by an opt-in run against a live account.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ocelhq/ocel/cloud/edge"
)

// ProjectTeardownPlan is what classifyProjectStacks resolves from the backend's
// full stack list: the project's one infra stack (empty when it has none yet)
// and every app-deploy stack it owns, including orphaned ones no promotion
// record still names.
type ProjectTeardownPlan struct {
	InfraStack string
	AppStacks  []string
}

// classifyProjectStacks splits the account-global backend's stack names into
// one project's teardown plan. A project owns every stack under the
// "<safeName(projectID)>--" prefix; the exact "<safeName>--infra" name is its
// infra stack and the rest are app-deploy stacks. The "--" delimiter keeps a
// project from matching a sibling whose id is a prefix of its own, and keeps
// production's "--" names off single-dash preview stacks. Pure.
func classifyProjectStacks(projectID string, stackNames []string) ProjectTeardownPlan {
	prefix := safeName(projectID) + "--"
	infra := InfraStackName(projectID)
	var plan ProjectTeardownPlan
	for _, name := range stackNames {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if name == infra {
			plan.InfraStack = name
			continue
		}
		plan.AppStacks = append(plan.AppStacks, name)
	}
	return plan
}

// DestroyProjectResult reports what DestroyProject settled that the host needs
// to act on beyond the joined error: whether the root stack is gone, so the
// host knows it is safe to forget the persisted root-stack state (deleting it
// while the root stack still stands would strip the identities a re-run needs
// to finish the teardown).
type DestroyProjectResult struct {
	RootTornDown bool
}

// DestroyProject tears a whole production project down, in reverse of deploy and
// traffic-first: the root stack (workers, custom domain, store) goes first so
// the site stops serving, then every app-deploy stack, then the stateful infra
// stack (deleting its databases and buckets outright — no snapshot), then the
// project's R2/S3 asset prefixes. stack/state may be zero when the project
// never reconciled a root stack, in which case there is nothing edge-side to
// remove and RootTornDown is reported true. Best-effort throughout.
func DestroyProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, projectID string, progress, log func(string)) (DestroyProjectResult, error) {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	var errs []error
	result := DestroyProjectResult{RootTornDown: true}

	if stack != nil && len(state) > 0 {
		report("Destroying root stack (workers, custom domain, store)")
		if err := stack.DestroyRootStack(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("destroy root stack: %w", err))
			result.RootTornDown = false
		}
	}

	plan, err := PlanProjectTeardown(ctx, cfg, projectID)
	if err != nil {
		errs = append(errs, err)
	}

	for _, name := range plan.AppStacks {
		report("Destroying app stack " + name)
		if err := Destroy(ctx, teardownConfig(cfg, name), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy app stack %s: %w", name, err))
		}
	}

	if plan.InfraStack != "" {
		report("Destroying infra stack " + plan.InfraStack + " (databases, buckets)")
		if err := Destroy(ctx, teardownConfig(cfg, plan.InfraStack), progress, log); err != nil {
			errs = append(errs, fmt.Errorf("destroy infra stack %s: %w", plan.InfraStack, err))
		}
	}

	report("Purging project assets")
	if err := purgeProjectAssets(ctx, cfg, projectID); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

// PlanProjectTeardown lists the account-global backend's stacks and classifies
// the ones this project owns. It opens a bare Pulumi workspace over the same
// self-managed backend Destroy selects against.
func PlanProjectTeardown(ctx context.Context, cfg Config, projectID string) (ProjectTeardownPlan, error) {
	ws, err := backendWorkspace(ctx, cfg.ProjectName, cfg.BackendURL, cfg.Passphrase, cfg.Region, cfg.Pulumi)
	if err != nil {
		return ProjectTeardownPlan{}, err
	}
	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return ProjectTeardownPlan{}, fmt.Errorf("list stacks: %w", err)
	}
	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	return classifyProjectStacks(projectID, names), nil
}

// purgeProjectAssets deletes a project's whole R2/S3 footprint: its static
// assets (in the adopted cache store) and its ISR/prerender entries (which land
// in both the asset bucket and the cache store), rooted at the project prefix
// so every app and build under it goes at once. Deleting a prefix nothing was
// written to is a no-op, mirroring Reclaim's per-build sweep at project scope.
func purgeProjectAssets(ctx context.Context, cfg Config, projectID string) error {
	assets := projectAssetR2Prefix(projectID)
	isr := projectISRPrefix(cfg.Env, projectID)
	var errs []error
	for _, t := range []struct {
		deleter PrefixDeleter
		bucket  string
		prefix  string
	}{
		{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, assets},
		{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, isr},
		{asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, isr},
	} {
		if err := deletePrefix(ctx, t.deleter, t.bucket, t.prefix); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// projectAssetR2Prefix is the static-assets prefix root under which every app
// and build of a project lives (appAssetR2Prefix without the app/build tail).
// The trailing slash keeps it from matching a sibling project whose id shares
// this one as a prefix.
func projectAssetR2Prefix(projectID string) string {
	return path.Join("assets", projectID) + "/"
}

// projectISRPrefix is the ISR/prerender prefix root for a project in one
// environment (appAssetPrefixFor without the app/build tail).
func projectISRPrefix(env, projectID string) string {
	return path.Join(env, projectID) + "/"
}

// teardownConfig projects the account-global Config onto the single-stack
// TeardownConfig Destroy selects with.
func teardownConfig(cfg Config, stackName string) TeardownConfig {
	return TeardownConfig{
		Region:      cfg.Region,
		BackendURL:  cfg.BackendURL,
		Passphrase:  cfg.Passphrase,
		ProjectName: cfg.ProjectName,
		StackName:   stackName,
		Pulumi:      cfg.Pulumi,
	}
}
