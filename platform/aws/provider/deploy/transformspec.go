package deploy

import (
	"context"
	"fmt"

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
	if err := transform.ResolveOutputs(ctx, spec, req, results); err != nil {
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
