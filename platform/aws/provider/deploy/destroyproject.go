package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ProjectTeardownPlan struct {
	InfraStack naming.StackName
	AppStacks  []naming.StackName
}

func classifyProjectStacks(stacks []naming.StackName) ProjectTeardownPlan {
	var plan ProjectTeardownPlan
	for _, stack := range stacks {
		if stack.Env != ProductionEnv {
			continue
		}
		if stack.IsInfra() {
			plan.InfraStack = stack
			continue
		}
		plan.AppStacks = append(plan.AppStacks, stack)
	}
	return plan
}

type DestroyProjectResult struct {
	EdgeTornDown bool
}

func destroyPhased(appStacks, infraStacks []naming.StackName, destroyApp, destroyInfra func(naming.StackName) error) (appErrs, infraErrs []error) {
	appErrs = runBounded(teardownConcurrency, appStacks, destroyApp)
	infraErrs = runBounded(teardownConcurrency, infraStacks, destroyInfra)
	return appErrs, infraErrs
}

type ProjectTeardownStages struct {
	Planning    Stage
	Unbind      Stage
	AppStacks   Stage
	InfraStacks Stage
	Edge        Stage
	Values      Stage
	Assets      Stage
	Forget      Stage
}

func (s ProjectTeardownStages) Roots() []Stage {
	return []Stage{s.Planning, s.Unbind, s.AppStacks, s.InfraStacks, s.Edge, s.Values, s.Assets, s.Forget}
}

func unbindRouting(ctx context.Context, stack edge.EdgeStack, cfg Config, stage Stage, pointers []string) (err error) {
	start := time.Now()
	defer func() { spanForStage(cfg.Tracer, stage, start, time.Now(), err) }()

	if stack == nil || len(stack.State()) == 0 {
		return nil
	}
	report := cfg.reportStage(stage)
	var errs []error
	for _, hostname := range edge.BoundDomains(stack.State()) {
		report(sanitizeMessage(fmt.Sprintf("Unbinding %s from the edge", hostname)))
		if err := stack.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before the origin it fronts is destroyed: %w", hostname, err))
		}
	}
	for _, pointer := range pointers {
		report(sanitizeMessage(fmt.Sprintf("Removing pointer %q from the store", pointer)))
		if _, err := stack.RemovePointer(ctx, pointer); err != nil {
			errs = append(errs, fmt.Errorf("remove pointer %q before the origin it points at is destroyed: %w", pointer, err))
		}
	}
	err = errors.Join(errs...)
	return err
}

func childStagesFor(parent Stage, stacks []naming.StackName) (map[naming.StackName]Stage, []Stage) {
	byStack := make(map[naming.StackName]Stage, len(stacks))
	ordered := make([]Stage, 0, len(stacks))
	for _, stack := range stacks {
		stage := NewStage(parent, stack.String())
		byStack[stack] = stage
		ordered = append(ordered, stage)
	}
	return byStack, ordered
}

func DestroyProject(ctx context.Context, stack edge.EdgeStack, cfg Config, slug string, stages ProjectTeardownStages, log func(string)) (DestroyProjectResult, error) {
	var errs []error
	result := DestroyProjectResult{EdgeTornDown: true}

	planStart := time.Now()
	indexed, err := indexedStacks(ctx, cfg.Stacks, slug)
	if err != nil {
		errs = append(errs, err)
	}
	plan := classifyProjectStacks(indexed)
	envs := purgeEnvs(indexed, cfg.Env)
	spanForStage(cfg.Tracer, stages.Planning, planStart, time.Now(), err)

	var infraStacks []naming.StackName
	if !plan.InfraStack.IsZero() {
		infraStacks = []naming.StackName{plan.InfraStack}
	}
	appChildren, appDeclared := childStagesFor(stages.AppStacks, plan.AppStacks)
	infraChildren, infraDeclared := childStagesFor(stages.InfraStacks, infraStacks)
	declareStages(cfg.Tracer, true, append(appDeclared, infraDeclared...)...)

	if err := unbindRouting(ctx, stack, cfg, stages.Unbind, []string{edge.DefaultPointer}); err != nil {
		errs = append(errs, err)
	}

	stacksStart := time.Now()
	appErrs, infraErrs := destroyPhased(plan.AppStacks, infraStacks,
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, cfg, stack, appChildren[stack], "app", log)
		},
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, cfg, stack, infraChildren[stack], "infra", log)
		})
	spanForStage(cfg.Tracer, stages.AppStacks, stacksStart, time.Now(), errors.Join(appErrs...))
	spanForStage(cfg.Tracer, stages.InfraStacks, stacksStart, time.Now(), errors.Join(infraErrs...))
	errs = append(errs, appErrs...)
	errs = append(errs, infraErrs...)

	edgeErr := destroyEdgeStack(ctx, stack, cfg, stages.Edge, "Destroying what the edge stack owns (surfaces, routes, deployments ledger)")
	if edgeErr != nil {
		result.EdgeTornDown = false
		errs = append(errs, edgeErr)
	}

	valuesStart := time.Now()
	verr := purgeProjectValues(ctx, cfg, slug, cfg.reportStage(stages.Values))
	spanForStage(cfg.Tracer, stages.Values, valuesStart, time.Now(), verr)
	if verr != nil {
		errs = append(errs, verr)
	}

	assetsStart := time.Now()
	cfg.reportStage(stages.Assets)("Purging project assets")
	aerr := purgeProjectAssets(ctx, cfg, slug, envs)
	spanForStage(cfg.Tracer, stages.Assets, assetsStart, time.Now(), aerr)
	if aerr != nil {
		errs = append(errs, aerr)
	}

	forgetStart := time.Now()
	ferr := forgetProject(ctx, cfg, slug, stages.Forget, errors.Join(errs...))
	spanForStage(cfg.Tracer, stages.Forget, forgetStart, time.Now(), ferr)
	if ferr != nil {
		errs = append(errs, ferr)
	}

	return result, errors.Join(errs...)
}

func forgetProject(ctx context.Context, cfg Config, slug string, stage Stage, unfinished error) error {
	report := cfg.reportStage(stage)
	if unfinished != nil {
		report("Leaving the project indexed: the rerun reads its progress from what is still here")
		return nil
	}
	report("Forgetting the project")
	return forgetProjectIfEmpty(ctx, cfg.Stacks, slug)
}

func destroyEdgeStack(ctx context.Context, stack edge.EdgeStack, cfg Config, stage Stage, what string) (err error) {
	start := time.Now()
	defer func() { spanForStage(cfg.Tracer, stage, start, time.Now(), err) }()

	if stack == nil || len(stack.State()) == 0 {
		return nil
	}
	report := cfg.reportStage(stage)
	report(what)
	prior := stack.State()
	if derr := stack.Destroy(ctx); derr != nil {
		err = fmt.Errorf("destroy the edge stack: %w", derr)
		return err
	}
	if rerr := releaseRecords(ctx, cfg, prior, report); rerr != nil {
		err = rerr
	}
	return err
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

func PlanProjectTeardown(ctx context.Context, cfg Config, slug string) (ProjectTeardownPlan, error) {
	stacks, err := indexedStacks(ctx, cfg.Stacks, slug)
	if err != nil {
		return ProjectTeardownPlan{}, err
	}
	return classifyProjectStacks(stacks), nil
}

func purgeEnvs(stacks []naming.StackName, env string) []string {
	envs := []string{env}
	for _, stack := range stacks {
		if !slices.Contains(envs, stack.Env) {
			envs = append(envs, stack.Env)
		}
	}
	slices.Sort(envs)
	return envs
}

func purgeProjectAssets(ctx context.Context, cfg Config, slug string, envs []string) error {
	var errs []error
	for _, env := range envs {
		prefix := projectEnvPrefix(env, slug)
		for _, t := range []prefixTarget{
			{asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, prefix},
			{asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, prefix},
			{asPrefixDeleter(cfg.Uploader), cfg.ArtifactBucket, prefix},
		} {
			if err := deletePrefix(ctx, t.deleter, t.bucket, t.prefix); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func projectEnvPrefix(env, slug string) string {
	return path.Join(env, naming.Sanitize(slug)) + "/"
}

const skipTeardownRefreshEnv = "OCEL_SKIP_TEARDOWN_REFRESH"

func skipTeardownRefresh() bool {
	switch strings.ToLower(os.Getenv(skipTeardownRefreshEnv)) {
	case "1", "true":
		return true
	}
	return false
}

func teardownConfig(cfg Config, stack naming.StackName) TeardownConfig {
	return TeardownConfig{
		Region:        cfg.Region,
		BackendURL:    cfg.BackendURL,
		Passphrase:    cfg.Passphrase,
		PulumiProject: cfg.PulumiProject,
		Project:       naming.Sanitize(cfg.Slug),
		Stack:         stack,
		Pulumi:        cfg.Pulumi,
		Stacks:        cfg.Stacks,
		SkipRefresh:   skipTeardownRefresh(),
		realized:      cfg.realized,
	}
}
