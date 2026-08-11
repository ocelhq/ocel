package deploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ProjectTeardownPlan struct {
	InfraStack string
	AppStacks  []string
}

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

type DestroyProjectResult struct {
	RootTornDown bool
}

func destroyPhased(appStacks, infraStacks []string, destroyApp, destroyInfra func(string) error) []error {
	errs := runBounded(teardownConcurrency, appStacks, destroyApp)
	return append(errs, runBounded(teardownConcurrency, infraStacks, destroyInfra)...)
}

func DestroyProject(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, progress, log func(string)) (DestroyProjectResult, error) {
	progress, log = serializeReports(progress, log)
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

	var infraStacks []string
	if plan.InfraStack != "" {
		infraStacks = []string{plan.InfraStack}
	}
	errs = append(errs, destroyPhased(plan.AppStacks, infraStacks,
		func(name string) error {
			report("Destroying app stack " + name)
			if err := Destroy(ctx, teardownConfig(cfg, name), progress, log); err != nil {
				return fmt.Errorf("destroy app stack %s: %w", name, err)
			}
			return nil
		},
		func(name string) error {
			report("Destroying infra stack " + name + " (databases, buckets)")
			if err := Destroy(ctx, teardownConfig(cfg, name), progress, log); err != nil {
				return fmt.Errorf("destroy infra stack %s: %w", name, err)
			}
			return nil
		})...)

	if err := purgeProjectValues(ctx, cfg, slug, report); err != nil {
		errs = append(errs, err)
	}

	report("Purging project assets")
	if err := purgeProjectAssets(ctx, cfg, slug); err != nil {
		errs = append(errs, err)
	}

	return result, errors.Join(errs...)
}

type ValueStore interface {
	Purge(ctx context.Context, slug string) (int, error)
}

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
	slices.Sort(sortedApps)
	for _, app := range sortedApps {
		add(workerScriptName(slug, env, app))
	}

	return names, nil
}

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

func purgeProjectAssets(ctx context.Context, cfg Config, slug string) error {
	assets := projectAssetR2Prefix(slug)
	isr := projectISRPrefix(cfg.Env, slug)
	var errs []error
	for _, t := range []prefixTarget{
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

func purgeProjectArtifacts(ctx context.Context, cfg Config, slug string) error {
	return deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.ArtifactBucket, projectArtifactPrefix(slug))
}

func projectAssetR2Prefix(slug string) string {
	return path.Join("assets", slug) + "/"
}

func projectImageConfigPrefix(slug string) string {
	return path.Join("image-config", slug) + "/"
}

func projectEdgeR2Prefix(slug string) string {
	return path.Join("edge", slug) + "/"
}

func projectArtifactPrefix(slug string) string {
	return slug + "/"
}

func projectISRPrefix(env, slug string) string {
	return path.Join(env, slug) + "/"
}

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
