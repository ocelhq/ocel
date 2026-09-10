package deploy

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

const (
	outputPlaceholderKey = "$ocelOutput"
	customBindingType    = "custom"
)

type outputRef struct {
	Type     string
	Name     string
	Property string
}

func (r outputRef) String() string {
	return fmt.Sprintf("bindings.%s.%s.%s", r.Type, r.Name, r.Property)
}

type outputSite struct {
	Resource string
	Surface  string
	Field    string
}

func (s outputSite) String() string {
	return fmt.Sprintf("%s's %s.%s", s.Resource, s.Surface, s.Field)
}

type placedOutput struct {
	Ref outputRef
	At  outputSite
}

type OutputPlaceholderError struct {
	At     outputSite
	Reason string
}

func (e *OutputPlaceholderError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s with a binding output that %s; author one as `bindings.<type>.<name>.<property>`",
		e.At, e.Reason)
}

type UnboundOutputError struct {
	Ref      outputRef
	At       outputSite
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
	Ref         outputRef
	At          outputSite
	Published   string
	Class       string
	Environment string
	Carries     []string
}

func (e *UnpublishedOutputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"a transform fills %s from %s, and nothing has published a record under %q to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		e.At, e.Ref, e.Published, describeCoordinate(e.Class, e.Environment))
	if len(e.Carries) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", describeCoordinate(e.Class, e.Environment))
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nPublished to %s: %s.", describeCoordinate(e.Class, e.Environment), strings.Join(e.Carries, ", "))
	return b.String()
}

type OutputPropertyError struct {
	Ref     outputRef
	At      outputSite
	Carries []string
}

func (e *OutputPropertyError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and the published record carries no such property. "+
			"The record carries %s — republish it with %s",
		e.At, e.Ref, carried(e.Carries), e.Ref.Property)
}

type ProvisionedOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *ProvisionedOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and this deploy provisions that %s itself. "+
			"Ocel publishes what it provisions long after the transforms have run, so its outputs are not there to read: "+
			"key %q under \"bindings\" in ocel.json if your own infrastructure owns it, or name one that is bound",
		e.At, e.Ref, e.Ref.Type, e.Ref.Name)
}

type EmptyOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *EmptyOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from %s, and the published record carries nothing under it. "+
			"A field an operator filled is never rendered as one they left alone, so this deploy stops here: republish it with a value under %s",
		e.At, e.Ref, e.Ref.Property)
}

func walkOutputs(candidates []transformCandidate, results []transform.Result, resolve func(outputRef, outputSite, any) (any, error)) error {
	for i, result := range results {
		for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
			patch := result.Patches[key]
			for _, field := range slices.Sorted(maps.Keys(patch)) {
				at := outputSite{Resource: candidates[i].key.Name, Surface: key, Field: field}
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

func mapOutputs(value any, at outputSite, resolve func(outputRef, outputSite, any) (any, error)) (any, error) {
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

func readOutputRef(m map[string]any, at outputSite) (outputRef, bool, error) {
	raw, named := m[outputPlaceholderKey]
	if !named {
		return outputRef{}, false, nil
	}
	if len(m) != 1 {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "carries keys beside the one it names"}
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names neither a binding nor a property"}
	}
	kind, _ := fields["type"].(string)
	name, _ := fields["name"].(string)
	property, _ := fields["property"].(string)
	if kind == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names no resource type"}
	}
	if name == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names the type %q and no binding under it", kind)}
	}
	if property == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names binding %s.%s and no property on it", kind, name)}
	}
	return outputRef{Type: kind, Name: name, Property: property}, true, nil
}

func carried(properties []string) string {
	if len(properties) == 0 {
		return "no properties at all"
	}
	return strings.Join(properties, ", ")
}

func emptyOutput(value any) bool {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return v.Len() == 0
	}
	return false
}
