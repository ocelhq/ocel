package deploy

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

func transformStackPlan(ctx context.Context, evaluator transform.Evaluator, plan providerkit.StackPlan) (*transformedArgs, error) {
	if evaluator == nil {
		return nil, nil
	}
	req := transform.Request{EnvClass: string(plan.Ref.Class), Env: plan.Ref.Name.Env}
	var candidates []transformCandidate

	for _, resource := range plan.Resources {
		name := resource.Name
		switch resource.Type {
		case providerkit.LinkPostgres:
			args := translatePostgres(postgresConfig(resource))
			req.Resources = append(req.Resources, transform.Resource{
				Type: "postgres", Name: name, Surfaces: postgresSurfaces(args),
			})
			candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
				applied, err := applyPostgresSurfaces(args, result)
				out.postgres[name] = applied
				return err
			}})
		case providerkit.LinkBucket:
			args := translateBucket(bucketConfig(resource))
			req.Resources = append(req.Resources, transform.Resource{
				Type: "bucket", Name: name, Surfaces: bucketSurfaces(args),
			})
			candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
				applied, err := applyBucketSurfaces(args, result)
				out.buckets[name] = applied
				return err
			}})
		}
	}

	if app := plan.App; app != nil {
		for _, spec := range app.Functions {
			name := spec.Name
			args := translateFunctionSpec(app.Framework, spec)
			req.Resources = append(req.Resources, transform.Resource{
				Type: "function", Name: name, App: app.App, Surfaces: functionSurfaces(args),
			})
			candidates = append(candidates, transformCandidate{name: name, apply: func(out *transformedArgs, result transform.Result) error {
				applied, err := applyFunctionSurfaces(args, result)
				out.functions[name] = applied
				return err
			}})
		}
	}

	results, err := evaluator.Evaluate(ctx, req)
	if err != nil {
		return nil, err
	}
	if results == nil {
		return nil, nil
	}
	if len(results) != len(candidates) {
		return nil, fmt.Errorf("transforms returned %d results for %d resources", len(results), len(candidates))
	}
	placed, err := resolvePlanOutputs(ctx, plan, candidates, results)
	if err != nil {
		return nil, err
	}

	out := &transformedArgs{
		functions: map[string]functionArgs{},
		buckets:   map[string]bucketArgs{},
		postgres:  map[string]postgresArgs{},
	}
	for i, c := range candidates {
		if err := c.apply(out, results[i]); err != nil {
			if named := nameOutputBehind(placed, c.name, err); named != nil {
				return nil, named
			}
			return nil, fmt.Errorf("transform %s: %w", c.name, err)
		}
	}
	return out, nil
}

func translateFunctionSpec(framework string, spec providerkit.FunctionSpec) functionArgs {
	runtime := defaultFunctionRuntime
	if spec.Runtime != "" {
		runtime = spec.Runtime
	}
	handler := defaultFunctionEntry
	if spec.Handler != "" {
		handler = spec.Handler
	}
	memoryMB := defaultFunctionMemoryMB
	if framework == frameworkNext {
		memoryMB = nextBundleFunctionMemoryMB
	}
	if spec.Memory > 0 {
		memoryMB = spec.Memory
	}
	args := functionArgs{
		Runtime:        runtime,
		Handler:        handler,
		MemorySizeMB:   memoryMB,
		TimeoutSeconds: defaultFunctionTimeoutSeconds,
		InvokeMode:     functionURLInvokeModeStream,
	}
	if spec.Timeout > 0 {
		args.TimeoutSeconds = int(spec.Timeout.Seconds())
	}
	return args
}

func resolvePlanOutputs(ctx context.Context, plan providerkit.StackPlan, candidates []transformCandidate, results []transform.Result) ([]placedOutput, error) {
	var placed []placedOutput
	if err := walkOutputs(candidates, results, func(ref outputRef, at outputSite, authored any) (any, error) {
		placed = append(placed, placedOutput{Ref: ref, At: at})
		return authored, nil
	}); err != nil {
		return nil, err
	}
	if len(placed) == 0 {
		return nil, nil
	}
	if err := refusePlanProvisionedOutputs(plan, placed); err != nil {
		return nil, err
	}
	values, err := readPlanOutputs(ctx, plan, placed)
	if err != nil {
		return nil, err
	}
	if err := walkOutputs(candidates, results, func(ref outputRef, _ outputSite, _ any) (any, error) {
		return values[ref], nil
	}); err != nil {
		return nil, err
	}
	return placed, nil
}

func refusePlanProvisionedOutputs(plan providerkit.StackPlan, placed []placedOutput) error {
	for _, p := range placed {
		if slices.ContainsFunc(plan.Resources, func(r providerkit.Resource) bool { return r.Name == p.Ref.Link }) {
			return &ProvisionedOutputError{Ref: p.Ref, At: p.At}
		}
	}
	return nil
}

func readPlanOutputs(ctx context.Context, plan providerkit.StackPlan, placed []placedOutput) (map[outputRef]any, error) {
	if plan.Links == nil {
		return nil, fmt.Errorf(
			"a transform fills %s from link %q, and this deploy reached no variable store to read published records from",
			placed[0].At, placed[0].Ref.Link)
	}
	published, err := plan.Links.Published(ctx)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from link %q: %w", placed[0].At, placed[0].Ref.Link, err)
	}
	names := make([]string, 0, len(published))
	for _, link := range published {
		names = append(names, link.Name)
	}
	slices.Sort(names)

	values := make(map[outputRef]any, len(placed))
	for _, p := range placed {
		if !slices.Contains(names, p.Ref.Link) {
			return nil, &UnpublishedOutputError{
				Ref: p.Ref, At: p.At, Class: string(plan.Ref.Class), Environment: plan.Ref.Name.Env, Published: names,
			}
		}
		if _, done := values[p.Ref]; done {
			continue
		}
		value, err := plan.Links.Resolve(ctx, p.Ref.Link, p.Ref.Property)
		if err != nil {
			return nil, &OutputPropertyError{Ref: p.Ref, At: p.At, Carries: carriedBy(published, p.Ref.Link)}
		}
		if emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: p.Ref, At: p.At}
		}
		values[p.Ref] = value
	}
	return values, nil
}

func carriedBy(published []providerkit.Link, name string) []string {
	for _, link := range published {
		if link.Name != name {
			continue
		}
		carries := make([]string, 0, len(link.Properties))
		for property := range link.Properties {
			carries = append(carries, property)
		}
		slices.Sort(carries)
		return carries
	}
	return nil
}
