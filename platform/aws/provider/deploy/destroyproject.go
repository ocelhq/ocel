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

func unbindRouting(ctx context.Context, stack edge.EdgeStack, rep Reporting, stage Stage, pointers []string) (err error) {
	start := time.Now()
	defer func() { spanForStage(rep.Tracer, stage, start, time.Now(), err) }()

	if stack == nil || stack.State().Empty() {
		return nil
	}
	report := rep.stage(stage)
	var errs []error
	for _, hostname := range stack.State().Bound {
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

func DestroyProject(ctx context.Context, stack edge.EdgeStack, t ProjectTeardown, stages ProjectTeardownStages, log func(string)) (DestroyProjectResult, error) {
	var errs []error
	result := DestroyProjectResult{EdgeTornDown: true}

	planStart := time.Now()
	indexed, err := indexedStacks(ctx, t.Stacks, t.Slug)
	if err != nil {
		errs = append(errs, err)
	}
	plan := classifyProjectStacks(indexed)
	envs := purgeEnvs(indexed, t.Env)
	spanForStage(t.Report.Tracer, stages.Planning, planStart, time.Now(), err)

	var infraStacks []naming.StackName
	if !plan.InfraStack.IsZero() {
		infraStacks = []naming.StackName{plan.InfraStack}
	}
	appChildren, appDeclared := childStagesFor(stages.AppStacks, plan.AppStacks)
	infraChildren, infraDeclared := childStagesFor(stages.InfraStacks, infraStacks)
	declareStages(t.Report.Tracer, true, append(appDeclared, infraDeclared...)...)

	if err := unbindRouting(ctx, stack, t.Report, stages.Unbind, []string{edge.DefaultPointer}); err != nil {
		errs = append(errs, err)
	}

	stacksStart := time.Now()
	appErrs, infraErrs := destroyPhased(plan.AppStacks, infraStacks,
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, t.Teardown, stack, appChildren[stack], "app", log)
		},
		func(stack naming.StackName) error {
			return destroyStackStage(ctx, t.Teardown, stack, infraChildren[stack], "infra", log)
		})
	spanForStage(t.Report.Tracer, stages.AppStacks, stacksStart, time.Now(), errors.Join(appErrs...))
	spanForStage(t.Report.Tracer, stages.InfraStacks, stacksStart, time.Now(), errors.Join(infraErrs...))
	errs = append(errs, appErrs...)
	errs = append(errs, infraErrs...)

	edgeErr := destroyEdgeStack(ctx, stack, t, stages.Edge, "Destroying what the edge stack owns (surfaces, routes, deployments ledger)")
	if edgeErr != nil {
		result.EdgeTornDown = false
		errs = append(errs, edgeErr)
	}

	valuesStart := time.Now()
	verr := purgeProjectValues(ctx, t.Values, t.Slug, t.Report.stage(stages.Values))
	spanForStage(t.Report.Tracer, stages.Values, valuesStart, time.Now(), verr)
	if verr != nil {
		errs = append(errs, verr)
	}

	assetsStart := time.Now()
	t.Report.stage(stages.Assets)("Purging project assets")
	aerr := purgeProjectAssets(ctx, t.Stores, t.Slug, envs)
	spanForStage(t.Report.Tracer, stages.Assets, assetsStart, time.Now(), aerr)
	if aerr != nil {
		errs = append(errs, aerr)
	}

	forgetStart := time.Now()
	ferr := forgetProject(ctx, t.Teardown, stages.Forget, errors.Join(errs...))
	spanForStage(t.Report.Tracer, stages.Forget, forgetStart, time.Now(), ferr)
	if ferr != nil {
		errs = append(errs, ferr)
	}

	return result, errors.Join(errs...)
}

func forgetProject(ctx context.Context, t Teardown, stage Stage, unfinished error) error {
	report := t.Report.stage(stage)
	if unfinished != nil {
		report("Leaving the project indexed: the rerun reads its progress from what is still here")
		return nil
	}
	report("Forgetting the project")
	return forgetProjectIfEmpty(ctx, t.Stacks, t.Slug)
}

func destroyEdgeStack(ctx context.Context, stack edge.EdgeStack, t ProjectTeardown, stage Stage, what string) (err error) {
	start := time.Now()
	defer func() { spanForStage(t.Report.Tracer, stage, start, time.Now(), err) }()

	if stack == nil || stack.State().Empty() {
		return nil
	}
	report := t.Report.stage(stage)
	report(what)
	prior := stack.State()
	if derr := stack.Destroy(ctx); derr != nil {
		err = fmt.Errorf("destroy the edge stack: %w", derr)
		return err
	}
	if rerr := releaseRecords(ctx, t.DNS, prior, report); rerr != nil {
		err = rerr
	}
	return err
}

type ValueStore interface {
	Purge(ctx context.Context, slug string) (int, error)
}

func purgeProjectValues(ctx context.Context, values ValueStore, slug string, report func(string)) error {
	if values == nil {
		return nil
	}
	nilSafe(report)("Removing the project's stored variable values")
	if _, err := values.Purge(ctx, slug); err != nil {
		return fmt.Errorf("remove %s's stored variable values: %w", slug, err)
	}
	return nil
}

func PlanProjectTeardown(ctx context.Context, index StackIndex, slug string) (ProjectTeardownPlan, error) {
	stacks, err := indexedStacks(ctx, index, slug)
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

func purgeProjectAssets(ctx context.Context, stores ObjectStores, slug string, envs []string) error {
	var errs []error
	for _, env := range envs {
		prefix := projectEnvPrefix(env, slug)
		for _, t := range []prefixTarget{
			{asPrefixDeleter(stores.CacheStoreUploader), stores.CacheStoreBucket, prefix},
			{asPrefixDeleter(stores.Uploader), stores.AssetBucket, prefix},
			{asPrefixDeleter(stores.Uploader), stores.ArtifactBucket, prefix},
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
