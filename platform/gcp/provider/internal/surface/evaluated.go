package surface

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const transformProvider = "gcp"

type Allowed map[string][]string

type resourceKey struct {
	kind string
	name string
}

type Evaluated struct {
	results map[resourceKey]transform.Result
}

func Request(tier environment.Tier, env string, resources []transform.Resource) transform.Request {
	return transform.Request{Provider: transformProvider, EnvTier: string(tier), Env: env, Resources: resources}
}

func Evaluate(ctx context.Context, pass transform.Pass, req transform.Request, allowed map[string]Allowed) (Evaluated, error) {
	return evaluate(ctx, pass, req, allowed, nil)
}

func EvaluateStack(ctx context.Context, pass transform.Pass, spec provider.StackSpec, resources []transform.Resource, allowed map[string]Allowed) (Evaluated, error) {
	req := Request(spec.Ref.Tier, spec.Ref.Name.Env, resources)
	return evaluate(ctx, pass, req, allowed, func(results []transform.Result) error {
		return transform.ResolveOutputs(ctx, spec, req, results)
	})
}

func evaluate(ctx context.Context, pass transform.Pass, req transform.Request, allowed map[string]Allowed, resolve func([]transform.Result) error) (Evaluated, error) {
	evaluated := Evaluated{results: map[resourceKey]transform.Result{}}
	if pass == nil || len(req.Resources) == 0 {
		return evaluated, nil
	}
	results, err := pass.Evaluate(ctx, req)
	if err != nil || results == nil {
		return evaluated, err
	}
	if len(results) != len(req.Resources) {
		return evaluated, fmt.Errorf("transforms returned %d results for %d resources", len(results), len(req.Resources))
	}
	for i, result := range results {
		if err := refuseUnreachable(req.Resources[i], result, allowed[req.Resources[i].Type]); err != nil {
			return evaluated, err
		}
	}
	if resolve != nil {
		if err := resolve(results); err != nil {
			return evaluated, err
		}
	}
	for i, result := range results {
		evaluated.results[resourceKey{kind: req.Resources[i].Type, name: req.Resources[i].Name}] = result
	}
	return evaluated, nil
}

func refuseUnreachable(resource transform.Resource, result transform.Result, allowed Allowed) error {
	named := resource.Type + " " + resource.Name
	for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
		paths, served := allowed[key]
		if !served {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s's %s, and this provider constructs no such resource for it: patch %s",
				named, key, strings.Join(slices.Sorted(maps.Keys(allowed)), ", "))
		}
		if err := refuseUnpatchable(named, key, paths, result.Patches[key]); err != nil {
			return err
		}
	}
	return RefuseInvalidLabels(named, result.Tags)
}

func (e Evaluated) For(kind, name string) transform.Result {
	return e.results[resourceKey{kind: kind, name: name}]
}

type evaluatedKey struct{}

type stackEvaluated struct {
	ref       provider.StackRef
	evaluated Evaluated
}

func WithEvaluated(ctx context.Context, ref provider.StackRef, evaluated Evaluated) context.Context {
	return context.WithValue(ctx, evaluatedKey{}, stackEvaluated{ref: ref, evaluated: evaluated})
}

func ReadEvaluated(ctx context.Context, ref provider.StackRef) (Evaluated, bool) {
	attached, found := ctx.Value(evaluatedKey{}).(stackEvaluated)
	if !found || attached.ref != ref {
		return Evaluated{}, false
	}
	return attached.evaluated, true
}

func RefuseUnevaluated(ctx context.Context, ref provider.StackRef, resource provider.Resource) error {
	if _, found := ReadEvaluated(ctx, ref); !found {
		return fmt.Errorf("the transforms for %s were not evaluated before %s %s was reached", ref.Name, resource.Type, resource.Name)
	}
	return nil
}

func SplitOutputs(patch map[string]any) (map[string]any, []string) {
	var unknown []string
	known := splitOutputs("", patch, &unknown)
	slices.Sort(unknown)
	return known, unknown
}

func (s Surface[D, U]) SplitOutputs(patch map[string]any) (map[string]any, []string) {
	known := patch
	var unknown []string
	for _, field := range s.Fields {
		for _, path := range field.Paths {
			if value, present := at(known, path.name); present && holdsOutput(value) {
				known = without(known, strings.Split(path.name, "."))
				unknown = append(unknown, path.name)
			}
		}
	}
	slices.Sort(unknown)
	return known, unknown
}

func ListPriced(unknown []string, priced map[string]string) []string {
	var listed []string
	for _, path := range unknown {
		if property, found := priced[path]; found {
			listed = append(listed, property)
		}
	}
	return listed
}

func without(patch map[string]any, keys []string) map[string]any {
	trimmed := maps.Clone(patch)
	if len(keys) == 1 {
		delete(trimmed, keys[0])
		return trimmed
	}
	if rest := without(trimmed[keys[0]].(map[string]any), keys[1:]); len(rest) > 0 {
		trimmed[keys[0]] = rest
	} else {
		delete(trimmed, keys[0])
	}
	return trimmed
}

func splitOutputs(prefix string, patch map[string]any, unknown *[]string) map[string]any {
	known := make(map[string]any, len(patch))
	for key, value := range patch {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		nested, isObject := value.(map[string]any)
		list, isList := value.([]any)
		switch {
		case transform.IsOutput(value) || isList && holdsOutput(list):
			*unknown = append(*unknown, path)
		case isObject:
			known[key] = splitOutputs(path, nested, unknown)
		default:
			known[key] = value
		}
	}
	return known
}

func holdsOutput(value any) bool {
	if transform.IsOutput(value) {
		return true
	}
	switch typed := value.(type) {
	case []any:
		return slices.ContainsFunc(typed, holdsOutput)
	case map[string]any:
		for _, inner := range typed {
			if holdsOutput(inner) {
				return true
			}
		}
	}
	return false
}
