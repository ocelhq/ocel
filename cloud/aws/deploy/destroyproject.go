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
	"sort"
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
// "<safeName(slug)>--" prefix; the exact "<safeName>--infra" name is its
// infra stack and the rest are app-deploy stacks. The "--" delimiter keeps a
// project from matching a sibling whose id is a prefix of its own, and keeps
// production's "--" names off single-dash preview stacks. Pure.
func classifyProjectStacks(slug string, stackNames []string) ProjectTeardownPlan {
	prefix := safeName(slug) + "--"
	infra := InfraStackName(slug)
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
func DestroyProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, progress, log func(string)) (DestroyProjectResult, error) {
	report := nilSafe(progress)

	var errs []error
	result := DestroyProjectResult{RootTornDown: true}

	if stack != nil && len(state) > 0 {
		report("Destroying root stack (workers, custom domain)")
		workers, err := rootStackWorkerNames(ctx, stack, state, slug, cfg.Env)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve root-stack workers: %w", err))
			result.RootTornDown = false
		} else if err := stack.DestroyRootStack(ctx, workers); err != nil {
			errs = append(errs, fmt.Errorf("destroy root stack: %w", err))
			result.RootTornDown = false
		}
		// The shared deployments-store worker outlives the project; only its own
		// instance is wiped. Done after the workers so the promotion history the
		// name enumeration read stays intact if that step failed and re-runs.
		report("Wiping the project's deployments-store instance")
		if err := stack.DestroyInstance(ctx, state); err != nil {
			errs = append(errs, fmt.Errorf("destroy deployments-store instance: %w", err))
			result.RootTornDown = false
		}
	}

	plan, err := PlanProjectTeardown(ctx, cfg, slug)
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

	if err := purgeProjectValues(ctx, cfg, slug, report); err != nil {
		errs = append(errs, err)
	}

	report("Purging project assets")
	if err := purgeProjectAssets(ctx, cfg, slug); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

// ValueStore is the variable store as a teardown sees it: one call that empties
// everything a project holds in this substrate's env class.
type ValueStore interface {
	Purge(ctx context.Context, slug string) (int, error)
}

// purgeProjectValues removes a destroyed project's stored variable values —
// current values and version history alike, for this substrate's class only.
// Leaving them would leave secrets in a table the operator believes they
// emptied, and nothing else would ever reclaim them: the store holds no
// reference to the stacks and assets the rest of the teardown removes.
//
// It runs after the compute is gone, so nothing is left reading a value while
// it disappears, and like every other step it is best-effort: its failure is
// returned for the caller to join and report, never to abort the teardown, and
// a re-run empties whatever the failure left.
func purgeProjectValues(ctx context.Context, cfg Config, slug string, report func(string)) error {
	if cfg.Values == nil {
		return nil
	}
	nilSafe(report)("Removing the project's stored variable values")
	if _, err := cfg.Values.Purge(ctx, slug); err != nil {
		return fmt.Errorf("remove %s's stored variable values: %w", slug, err)
	}
	return nil
}

// rootStackWorkerNames resolves the exact set of edge workers a project's root
// stack deployed, so DestroyRootStack deletes precisely those and never has to
// guess a project's workers from a name prefix (which could collide with a
// sibling project). The no-app "root" generic worker is deterministic from the
// project; the legacy unqualified name (from before workers were named per app)
// is included so an old single-worker project is reclaimed too. The per-app
// generic workers are named from the app set, which the store's own promotion
// history carries, keyed by app. The shared deployments-store worker is never
// in this set — it outlives the project, and DestroyProject wipes only the
// project's own instance of it (DestroyInstance).
func rootStackWorkerNames(ctx context.Context, stack edge.RootStack, state edge.RootStackState, slug, env string) ([]string, error) {
	prodStack := slug + "-" + env

	history, err := stack.History(ctx, state, "")
	if err != nil {
		return nil, err
	}
	apps := map[string]struct{}{}
	for _, h := range history {
		for app := range h.Builds {
			apps[app] = struct{}{}
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

	add(legacyWorkerName(prodStack))
	add(workerScriptName(slug, env, "root"))
	sortedApps := make([]string, 0, len(apps))
	for app := range apps {
		sortedApps = append(sortedApps, app)
	}
	sort.Strings(sortedApps)
	for _, app := range sortedApps {
		add(workerScriptName(slug, env, app))
	}

	return names, nil
}

// PlanProjectTeardown lists the account-global backend's stacks and classifies
// the ones this project owns. It opens a bare Pulumi workspace over the same
// self-managed backend Destroy selects against.
func PlanProjectTeardown(ctx context.Context, cfg Config, slug string) (ProjectTeardownPlan, error) {
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
	return classifyProjectStacks(slug, names), nil
}

// purgeProjectAssets deletes a project's whole R2/S3 footprint: its edge
// bundles (in the adopted cache store), its static assets and ISR/prerender
// entries (which land in both the asset bucket and the cache store), its image
// configs (asset bucket only), and its function deployment artifacts, rooted at
// the project prefix so every app and build under it goes at once. Deleting a
// prefix nothing was written to is a no-op, mirroring Reclaim's per-build sweep
// at project scope.
func purgeProjectAssets(ctx context.Context, cfg Config, slug string) error {
	assets := projectAssetR2Prefix(slug)
	isr := projectISRPrefix(cfg.Env, slug)
	var errs []error
	for _, t := range []struct {
		deleter PrefixDeleter
		bucket  string
		prefix  string
	}{
		{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, assets},
		{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, projectEdgeR2Prefix(slug)},
		{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, isr},
		{asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, assets},
		{asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, projectImageConfigPrefix(slug)},
		{asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, isr},
	} {
		if err := deletePrefix(ctx, t.deleter, t.bucket, t.prefix); err != nil {
			errs = append(errs, err)
		}
	}
	if err := purgeProjectArtifacts(ctx, cfg, slug); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// purgeProjectArtifacts deletes every function deployment artifact a project
// uploaded to its substrate's artifact bucket. The artifact bucket's own
// expire-artifacts lifecycle rule would reap them eventually — this reclaims
// the storage at teardown instead of a month later. Safe only at whole-project
// scope: artifactKey is content-addressed and carries no env, pointer or build
// segment, so two deployments running identical code share one object and only
// the project prefix bounds a set nothing live still points at.
func purgeProjectArtifacts(ctx context.Context, cfg Config, slug string) error {
	return deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.ArtifactBucket, projectArtifactPrefix(slug))
}

// projectAssetR2Prefix is the static-assets prefix root under which every app
// and build of a project lives (appAssetR2Prefix without the app/build tail).
// The trailing slash keeps it from matching a sibling project whose id shares
// this one as a prefix.
func projectAssetR2Prefix(slug string) string {
	return path.Join("assets", slug) + "/"
}

// projectImageConfigPrefix is the image-config prefix root under which every
// app and build of a project lives (imageConfigKey without the app/build tail),
// trailing-slashed for the same reason projectAssetR2Prefix is. Asset bucket
// only — the config is never mirrored to the cache store.
func projectImageConfigPrefix(slug string) string {
	return path.Join("image-config", slug) + "/"
}

// projectEdgeR2Prefix is the edge-bundle prefix root under which every app and
// build of a project lives (appEdgeR2Prefix without the app/build tail),
// trailing-slashed for the same reason projectAssetR2Prefix is.
func projectEdgeR2Prefix(slug string) string {
	return path.Join("edge", slug) + "/"
}

// projectArtifactPrefix is the function-artifact prefix root under which every
// function and content hash of a project lives (artifactKey without the
// function/hash tail), trailing-slashed for the same reason
// projectAssetR2Prefix is.
func projectArtifactPrefix(slug string) string {
	return slug + "/"
}

// projectISRPrefix is the ISR/prerender prefix root for a project in one
// environment (appAssetPrefixFor without the app/build tail).
func projectISRPrefix(env, slug string) string {
	return path.Join(env, slug) + "/"
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
