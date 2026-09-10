package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

func transformStackPlan(ctx context.Context, evaluator transform.Evaluator, plan providerkit.StackPlan) (*transformPatches, error) {
	if evaluator == nil {
		return nil, nil
	}
	project, stack := naming.Sanitize(plan.Ref.Project), plan.Ref.Name
	req := transform.Request{
		Provider: transform.Provider,
		EnvClass: string(plan.Ref.Class),
		Env:      stack.Env,
	}
	var candidates []transformCandidate

	if app := plan.App; app != nil {
		for _, spec := range app.Functions {
			req.Resources = append(req.Resources, transform.Resource{
				Type: transformTypeFunction, Name: spec.Name, App: app.App,
			})
			candidates = append(candidates, transformCandidate{
				key:   resourceKey{Type: transformTypeFunction, Name: spec.Name},
				names: functionResourceNames(project, stack, spec.Name),
			})
		}
	} else {
		for _, resource := range plan.Resources {
			if resource.Binding != "" {
				continue
			}
			switch resource.Type {
			case providerkit.BindingPostgres:
				req.Resources = append(req.Resources, transform.Resource{Type: transformTypePostgres, Name: resource.Name})
				candidates = append(candidates, transformCandidate{
					key:   resourceKey{Type: transformTypePostgres, Name: resource.Name},
					names: postgresResourceNames(project, stack.Env, resource.Name),
				})
			case providerkit.BindingBucket:
				req.Resources = append(req.Resources, transform.Resource{Type: transformTypeBucket, Name: resource.Name})
				candidates = append(candidates, transformCandidate{
					key:   resourceKey{Type: transformTypeBucket, Name: resource.Name},
					names: bucketResourceNames(project, stack.Env, resource.Name),
				})
			}
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
	if err := resolvePlanOutputs(ctx, plan, candidates, results); err != nil {
		return nil, err
	}
	return indexPatches(candidates, results)
}

func translateFunctionSpec(runtime string, spec providerkit.FunctionSpec) (functionArgs, error) {
	execution, err := executionFor(spec.Runtime)
	if err != nil {
		return functionArgs{}, err
	}
	handler := defaultFunctionEntry
	if spec.Handler != "" {
		handler = spec.Handler
	}
	memoryMB := defaultFunctionMemoryMB
	if runtime == runtimeNext {
		memoryMB = nextBundleFunctionMemoryMB
	}
	if spec.Memory > 0 {
		memoryMB = spec.Memory
	}
	args := functionArgs{
		Runtime:        execution.Runtime,
		Handler:        handler,
		Arch:           execution.Arch,
		MemorySizeMB:   memoryMB,
		TimeoutSeconds: defaultFunctionTimeoutSeconds,
		InvokeMode:     functionURLInvokeModeStream,
	}
	if spec.Timeout > 0 {
		args.TimeoutSeconds = int(spec.Timeout.Seconds())
	}
	return args, nil
}

type execution struct {
	Runtime string
	Arch    string
}

func executionFor(runtime providerkit.Runtime) (execution, error) {
	if runtime.Name != "" && !providerkit.KnownRuntime(runtime.Name) {
		return execution{}, providerkit.Refuse(providerkit.CodeInvalid, "this provider has no runtime named %q", runtime.Name)
	}
	arch := providerkit.Architecture(runtime.Arch)
	if arch != providerkit.ArchX8664 && arch != providerkit.ArchARM64 {
		return execution{}, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider runs functions on %s and %s, and %q asks for %s",
			providerkit.ArchX8664, providerkit.ArchARM64, runtime.Name, runtime.Arch)
	}
	return execution{Runtime: managedRuntime(runtime.Name), Arch: arch}, nil
}

func managedRuntime(name string) string {
	switch name {
	case providerkit.RuntimePython:
		return pythonFunctionRuntime
	case "", providerkit.RuntimeNode, providerkit.RuntimeNext:
		return defaultFunctionRuntime
	}
	return providedFunctionRuntime
}

func resolvePlanOutputs(ctx context.Context, plan providerkit.StackPlan, candidates []transformCandidate, results []transform.Result) error {
	var placed []placedOutput
	if err := walkOutputs(candidates, results, func(ref outputRef, at outputSite, authored any) (any, error) {
		placed = append(placed, placedOutput{Ref: ref, At: at})
		return authored, nil
	}); err != nil {
		return err
	}
	if len(placed) == 0 {
		return nil
	}
	values, err := readPlanOutputs(ctx, plan, placed)
	if err != nil {
		return err
	}
	return walkOutputs(candidates, results, func(ref outputRef, _ outputSite, _ any) (any, error) {
		return values[ref], nil
	})
}

func publishedAs(plan providerkit.StackPlan, ref outputRef, at outputSite) (string, error) {
	if ref.Type == customBindingType {
		return ref.Name, nil
	}
	for _, resource := range plan.Resources {
		if string(resource.Type) != ref.Type || resource.Name != ref.Name {
			continue
		}
		if resource.Binding == "" {
			return "", &ProvisionedOutputError{Ref: ref, At: at}
		}
		return resource.Binding, nil
	}
	return "", &UnboundOutputError{Ref: ref, At: at, Declared: declaredBindings(plan)}
}

func declaredBindings(plan providerkit.StackPlan) []string {
	var out []string
	for _, resource := range plan.Resources {
		if resource.Binding == "" {
			continue
		}
		out = append(out, string(resource.Type)+"."+resource.Name)
	}
	slices.Sort(out)
	return out
}

func readPlanOutputs(ctx context.Context, plan providerkit.StackPlan, placed []placedOutput) (map[outputRef]any, error) {
	published := make(map[outputRef]string, len(placed))
	for _, p := range placed {
		name, err := publishedAs(plan, p.Ref, p.At)
		if err != nil {
			return nil, err
		}
		published[p.Ref] = name
	}

	if plan.Bindings == nil {
		return nil, fmt.Errorf(
			"a transform fills %s from %s, and this deploy reached no variable store to read published records from",
			placed[0].At, placed[0].Ref)
	}
	names, err := plan.Bindings.Names(ctx)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from %s: %w", placed[0].At, placed[0].Ref, err)
	}
	slices.Sort(names)

	wanted := make([]string, 0, len(placed))
	for _, p := range placed {
		name := published[p.Ref]
		if !slices.Contains(names, name) {
			return nil, &UnpublishedOutputError{
				Ref: p.Ref, At: p.At, Published: name,
				Class: string(plan.Ref.Class), Environment: plan.Ref.Name.Env, Carries: names,
			}
		}
		if !slices.Contains(wanted, name) {
			wanted = append(wanted, name)
		}
	}
	records, err := resolvePlanBindings(ctx, plan.Bindings, wanted)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from %s: %w", placed[0].At, placed[0].Ref, err)
	}

	values := make(map[outputRef]any, len(placed))
	for _, p := range placed {
		if _, done := values[p.Ref]; done {
			continue
		}
		record := records[published[p.Ref]]
		value, carries := record.Properties[p.Ref.Property]
		if !carries {
			return nil, &OutputPropertyError{Ref: p.Ref, At: p.At, Carries: slices.Sorted(maps.Keys(record.Properties))}
		}
		if emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: p.Ref, At: p.At}
		}
		values[p.Ref] = value
	}
	return values, nil
}

func resolvePlanBindings(ctx context.Context, bindings providerkit.BindingReader, names []string) (map[string]providerkit.Binding, error) {
	held := make([]providerkit.Binding, len(names))
	group, gctx := errgroup.WithContext(ctx)
	for i, name := range names {
		group.Go(func() error {
			record, err := bindings.Resolve(gctx, name)
			if err != nil {
				return err
			}
			held[i] = record
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	records := make(map[string]providerkit.Binding, len(names))
	for i, name := range names {
		records[name] = held[i]
	}
	return records, nil
}
