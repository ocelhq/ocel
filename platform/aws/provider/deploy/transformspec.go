package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
)

//go:generate go generate -C ../../../../pkg/provider/transform ./...

const transformProvider = "aws"

func NodePass(root string, modules []string) transform.NodePass {
	return transform.NodePass{
		Root: root, Modules: modules,
		External:    []string{"@pulumi/*"},
		Uninstalled: "`@ocel/transforms` brings in `@pulumi/aws` itself: install it as a devDependency and re-run.",
	}
}

func transformStackSpec(ctx context.Context, namespace, account string, pass transform.Pass, registry component.Registry, spec provider.StackSpec) (*transformPatches, error) {
	if pass == nil {
		return nil, nil
	}
	project, stack := naming.Sanitize(spec.Ref.Project), spec.Ref.Name
	req := transform.Request{
		Provider: transformProvider,
		EnvTier:  string(spec.Ref.Tier),
		Env:      stack.Env,
	}
	var candidates []transformCandidate

	if app := spec.App; app != nil && app.Compute == provider.ComputeContainer {
		names, err := container.NamesFor(namespace, project, stack)
		if err != nil {
			return nil, err
		}
		req.Resources = append(req.Resources, transform.Resource{Type: transformTypeContainer, Name: app.App, App: app.App})
		candidates = append(candidates, transformCandidate{
			key:   resourceKey{Type: transformTypeContainer, Name: app.App},
			names: names.Surfaces(),
		})
	} else if app != nil {
		for _, spec := range app.Functions {
			candidate, err := functionCandidate(namespace, project, stack, spec.Name)
			if err != nil {
				return nil, err
			}
			req.Resources = append(req.Resources, transform.Resource{
				Type: transformTypeFunction, Name: spec.Name, App: app.App,
			})
			candidates = append(candidates, candidate)
		}
	} else {
		at := component.Stack{Ref: spec.Ref, Namespace: namespace, Project: project, Env: stack.Env}
		for _, resource := range spec.Resources {
			if resource.Binding != "" {
				continue
			}
			shown, candidate, patchable, err := newInfraTransformCandidate(registry, at, resource)
			if err != nil {
				return nil, err
			}
			if patchable {
				req.Resources = append(req.Resources, shown)
				candidates = append(candidates, candidate)
			}
		}
	}

	results, err := pass.Evaluate(ctx, req)
	if err != nil {
		return nil, err
	}
	if results == nil {
		return nil, nil
	}
	if len(results) != len(candidates) {
		return nil, fmt.Errorf("transforms returned %d results for %d resources", len(results), len(candidates))
	}
	if err := resolveSpecOutputs(ctx, spec, candidates, results); err != nil {
		return nil, err
	}
	managed := map[resourceKey][]string{}
	for i, candidate := range candidates {
		patches, arns, err := takeManagedPolicies(candidate.key, results[i].Patches, account)
		if err != nil {
			return nil, err
		}
		results[i].Patches = patches
		if len(arns) > 0 {
			managed[candidate.key] = arns
		}
	}
	indexed, err := indexPatches(candidates, results)
	if err != nil {
		return nil, err
	}
	indexed.managed = managed
	return indexed, nil
}

func functionCandidate(namespace, project string, stack naming.StackName, name string) (transformCandidate, error) {
	names, err := function.NamesFor(namespace, project, stack, name)
	if err != nil {
		return transformCandidate{}, err
	}
	return transformCandidate{
		key:   resourceKey{Type: transformTypeFunction, Name: name},
		names: names.Surfaces(),
		check: func(name string, patches map[string]map[string]any) error {
			return checkVPCConfig(name, patches["lambda"])
		},
	}, nil
}

func resolveSpecOutputs(ctx context.Context, spec provider.StackSpec, candidates []transformCandidate, results []transform.Result) error {
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
	values, err := readSpecOutputs(ctx, spec, placed)
	if err != nil {
		return err
	}
	return walkOutputs(candidates, results, func(ref outputRef, at outputSite, _ any) (any, error) {
		value, resolved := values[ref]
		if !resolved || emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: ref, At: at}
		}
		return value, nil
	})
}

func publishedAs(spec provider.StackSpec, ref outputRef, at outputSite) (string, error) {
	if ref.Type == customBindingType {
		return ref.Name, nil
	}
	for _, resource := range spec.Resources {
		if string(resource.Type) != ref.Type || resource.Name != ref.Name {
			continue
		}
		if resource.Binding == "" {
			return "", &ProvisionedOutputError{Ref: ref, At: at}
		}
		return resource.Binding, nil
	}
	return "", &UnboundOutputError{Ref: ref, At: at, Declared: declaredBindings(spec)}
}

func declaredBindings(spec provider.StackSpec) []string {
	var out []string
	for _, resource := range spec.Resources {
		if resource.Binding == "" {
			continue
		}
		out = append(out, string(resource.Type)+"."+resource.Name)
	}
	slices.Sort(out)
	return out
}

func readSpecOutputs(ctx context.Context, spec provider.StackSpec, placed []placedOutput) (map[outputRef]any, error) {
	published := make(map[outputRef]string, len(placed))
	for _, p := range placed {
		name, err := publishedAs(spec, p.Ref, p.At)
		if err != nil {
			return nil, err
		}
		published[p.Ref] = name
	}

	if spec.Bindings == nil {
		return nil, fmt.Errorf(
			"a transform fills %s from %s, and this deploy reached no variable store to read published records from",
			placed[0].At, placed[0].Ref)
	}
	names, err := spec.Bindings.Names(ctx)
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
				Tier: string(spec.Ref.Tier), Environment: spec.Ref.Name.Env, Properties: names,
			}
		}
		if !slices.Contains(wanted, name) {
			wanted = append(wanted, name)
		}
	}
	records, err := resolveSpecBindings(ctx, spec.Bindings, wanted)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from %s: %w", placed[0].At, placed[0].Ref, err)
	}

	values := make(map[outputRef]any, len(placed))
	for _, p := range placed {
		if _, done := values[p.Ref]; done {
			continue
		}
		record := records[published[p.Ref]]
		value, present := record.Properties[p.Ref.Property]
		if !present {
			return nil, &OutputPropertyError{Ref: p.Ref, At: p.At, Properties: slices.Sorted(maps.Keys(record.Properties))}
		}
		if emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: p.Ref, At: p.At}
		}
		values[p.Ref] = value
	}
	return values, nil
}

func resolveSpecBindings(ctx context.Context, bindings provider.Bindings, names []string) (map[string]provider.Binding, error) {
	resolved := make([]provider.Binding, len(names))
	group, gctx := errgroup.WithContext(ctx)
	for i, name := range names {
		group.Go(func() error {
			record, err := bindings.Named(gctx, name)
			if err != nil {
				return err
			}
			resolved[i] = record
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	records := make(map[string]provider.Binding, len(names))
	for i, name := range names {
		records[name] = resolved[i]
	}
	return records, nil
}

func newInfraTransformCandidate(registry component.Registry, at component.Stack, resource provider.Resource) (transform.Resource, transformCandidate, bool, error) {
	transformed, served := registry.For(resource.Type)
	if !served || transformed.Transform == nil {
		return transform.Resource{}, transformCandidate{}, false, nil
	}
	target, err := transformed.Transform(at, resource)
	if err != nil {
		return transform.Resource{}, transformCandidate{}, false, err
	}
	key := resourceKey{Type: target.Kind, Name: resource.Name}
	return transform.Resource{Type: target.Kind, Name: resource.Name}, transformCandidate{key: key, names: target.Names, check: target.Check}, true, nil
}
