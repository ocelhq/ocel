package transform

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	outputPlaceholderKey = "$ocelOutput"
	customBindingType    = "custom"
)

type OutputRef struct {
	Type     string
	Name     string
	Property string
}

func (r OutputRef) String() string {
	return fmt.Sprintf("bindings.%s.%s.%s", r.Type, r.Name, r.Property)
}

type OutputSite struct {
	Resource string
	Surface  string
	Field    string
}

func (s OutputSite) String() string {
	return fmt.Sprintf("%s's %s.%s", s.Resource, s.Surface, s.Field)
}

type placedOutput struct {
	Ref OutputRef
	At  OutputSite
}

type OutputPlaceholderError struct {
	At     OutputSite
	Reason string
}

func (e *OutputPlaceholderError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s with a binding output that %s; author one as `bindings.<type>.<name>.<property>`",
		e.At, e.Reason)
}

type UnboundOutputError struct {
	Ref      OutputRef
	At       OutputSite
	Declared []string
}

func (e *UnboundOutputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"a transform fills %s from %s, and this project binds no %s named %q. "+
			"A binding output reads the record your config binds under that name, so key it under \"bindings\" in ocel.json, "+
			"or read a record nothing declared as `bindings.custom.%s.%s`",
		e.At, e.Ref, e.Ref.Type, e.Ref.Name, e.Ref.Name, e.Ref.Property)
	if len(e.Declared) == 0 {
		fmt.Fprint(&b, "\n\nThis project binds nothing at all.")
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nBound: %s.", strings.Join(e.Declared, ", "))
	return b.String()
}

type UnpublishedOutputError struct {
	Ref         OutputRef
	At          OutputSite
	Published   string
	Tier        string
	Environment string
	Properties  []string
}

func (e *UnpublishedOutputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"a transform fills %s from %s, and nothing has published a record under %q to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		e.At, e.Ref, e.Published, describeCoordinate(e.Tier, e.Environment))
	if len(e.Properties) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", describeCoordinate(e.Tier, e.Environment))
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nPublished to %s: %s.", describeCoordinate(e.Tier, e.Environment), strings.Join(e.Properties, ", "))
	return b.String()
}

func describeCoordinate(tier, environment string) string {
	if environment == "" {
		return tier
	}
	return tier + "/" + environment
}

type OutputPropertyError struct {
	Ref        OutputRef
	At         OutputSite
	Properties []string
}

func (e *OutputPropertyError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and the published record has no such property. "+
			"The record has %s — republish it with %s",
		e.At, e.Ref, listProperties(e.Properties), e.Ref.Property)
}

type ProvisionedOutputError struct {
	Ref OutputRef
	At  OutputSite
}

func (e *ProvisionedOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and this deploy provisions that %s itself. "+
			"Ocel publishes what it provisions long after the transforms have run, so its outputs are not there to read: "+
			"key %q under \"bindings\" in ocel.json if your own infrastructure owns it, or name one that is bound",
		e.At, e.Ref, e.Ref.Type, e.Ref.Name)
}

type EmptyOutputError struct {
	Ref OutputRef
	At  OutputSite
}

func (e *EmptyOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and the published record has nothing under it. "+
			"A field an operator filled is never rendered as one they left alone, so this deploy stops here: republish it with a value under %s",
		e.At, e.Ref, e.Ref.Property)
}

func IsOutput(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	_, named := m[outputPlaceholderKey]
	return named
}

func ResolveOutputs(ctx context.Context, spec provider.StackSpec, req Request, results []Result) error {
	var placed []placedOutput
	if err := WalkOutputs(req, results, func(ref OutputRef, at OutputSite, authored any) (any, error) {
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
	return WalkOutputs(req, results, func(ref OutputRef, at OutputSite, _ any) (any, error) {
		value, resolved := values[ref]
		if !resolved || emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: ref, At: at}
		}
		return value, nil
	})
}

func WalkOutputs(req Request, results []Result, resolve func(OutputRef, OutputSite, any) (any, error)) error {
	for i, result := range results {
		for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
			patch := result.Patches[key]
			for _, field := range slices.Sorted(maps.Keys(patch)) {
				at := OutputSite{Resource: req.Resources[i].Name, Surface: key, Field: field}
				resolved, err := mapOutputs(patch[field], at, resolve)
				if err != nil {
					return err
				}
				patch[field] = resolved
			}
		}
	}
	return nil
}

func mapOutputs(value any, at OutputSite, resolve func(OutputRef, OutputSite, any) (any, error)) (any, error) {
	switch t := value.(type) {
	case map[string]any:
		ref, named, err := readOutputRef(t, at)
		if err != nil {
			return nil, err
		}
		if named {
			return resolve(ref, at, value)
		}
		out := make(map[string]any, len(t))
		for key, item := range t {
			resolved, err := mapOutputs(item, at, resolve)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			resolved, err := mapOutputs(item, at, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	}
	return value, nil
}

func readOutputRef(m map[string]any, at OutputSite) (OutputRef, bool, error) {
	raw, named := m[outputPlaceholderKey]
	if !named {
		return OutputRef{}, false, nil
	}
	if len(m) != 1 {
		return OutputRef{}, false, &OutputPlaceholderError{At: at, Reason: "has keys beside the one it names"}
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return OutputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names neither a binding nor a property"}
	}
	kind, _ := fields["type"].(string)
	name, _ := fields["name"].(string)
	property, _ := fields["property"].(string)
	if kind == "" {
		return OutputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names no resource type"}
	}
	if name == "" {
		return OutputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names the type %q and no binding under it", kind)}
	}
	if property == "" {
		return OutputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names binding %s.%s and no property on it", kind, name)}
	}
	return OutputRef{Type: kind, Name: name, Property: property}, true, nil
}

func publishedAs(spec provider.StackSpec, ref OutputRef, at OutputSite) (string, error) {
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

func readSpecOutputs(ctx context.Context, spec provider.StackSpec, placed []placedOutput) (map[OutputRef]any, error) {
	published := make(map[OutputRef]string, len(placed))
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
	records, err := readBindings(ctx, spec.Bindings, wanted)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from %s: %w", placed[0].At, placed[0].Ref, err)
	}

	values := make(map[OutputRef]any, len(placed))
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

func readBindings(ctx context.Context, bindings provider.Bindings, names []string) (map[string]provider.Binding, error) {
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

func listProperties(properties []string) string {
	if len(properties) == 0 {
		return "no properties at all"
	}
	return strings.Join(properties, ", ")
}

func emptyOutput(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return v.Len() == 0
	}
	return false
}
