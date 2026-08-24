package deploy

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

const outputPlaceholderKey = "$ocelOutput"

type outputRef struct {
	Link     string
	Property string
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
		"a transform fills %s with a link output that %s; author one as `links.<name>.<property>` from a callback transform",
		e.At, e.Reason)
}

type UnpublishedOutputError struct {
	Ref         outputRef
	At          outputSite
	Class       string
	Environment string
	Published   []string
}

func (e *UnpublishedOutputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"a transform fills %s from link %q's %s, and nothing has published a record under that name to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		e.At, e.Ref.Link, e.Ref.Property, describeCoordinate(e.Class, e.Environment))
	if len(e.Published) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", describeCoordinate(e.Class, e.Environment))
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nPublished to %s: %s.", describeCoordinate(e.Class, e.Environment), strings.Join(e.Published, ", "))
	return b.String()
}

type OutputPropertyError struct {
	Ref     outputRef
	At      outputSite
	Carries []string
}

func (e *OutputPropertyError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and the published record carries no such property. "+
			"The record carries %s — republish it with %s",
		e.At, e.Ref.Link, e.Ref.Property, carried(e.Carries), e.Ref.Property)
}

type ProvisionedOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *ProvisionedOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and this deploy provisions %q itself. "+
			"Ocel publishes what it provisions long after the transforms have run, so its outputs are not there to read: "+
			"drop %q from the resource declarations and bind it through `links` if your own infrastructure owns it, or name a link that does",
		e.At, e.Ref.Link, e.Ref.Property, e.Ref.Link, e.Ref.Link)
}

type EmptyOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *EmptyOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and the published record carries nothing under it. "+
			"A field an operator filled is never rendered as one they left alone, so this deploy stops here: republish %q with a value under %s",
		e.At, e.Ref.Link, e.Ref.Property, e.Ref.Link, e.Ref.Property)
}

type OutputShapeError struct {
	Ref outputRef
	At  outputSite
	Err error
}

func (e *OutputShapeError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and the record's value is not what that field takes: %v",
		e.At, e.Ref.Link, e.Ref.Property, e.Err)
}

func (e *OutputShapeError) Unwrap() error { return e.Err }

func nameOutputBehind(placed []placedOutput, resource string, err error) error {
	var undecodable *surfaceFieldError
	if !errors.As(err, &undecodable) {
		return nil
	}
	for _, p := range placed {
		if p.At == (outputSite{Resource: resource, Surface: undecodable.Surface, Field: undecodable.Field}) {
			return &OutputShapeError{Ref: p.Ref, At: p.At, Err: err}
		}
	}
	return nil
}

func walkOutputs(candidates []transformCandidate, results []transform.Result, resolve func(outputRef, outputSite, any) (any, error)) error {
	for i, result := range results {
		for _, key := range slices.Sorted(maps.Keys(result.Surfaces)) {
			args := result.Surfaces[key]
			for _, field := range slices.Sorted(maps.Keys(args)) {
				at := outputSite{Resource: candidates[i].name, Surface: key, Field: field}
				resolved, err := mapOutputs(args[field], at, resolve)
				if err != nil {
					return err
				}
				args[field] = resolved
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
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names neither a link nor a property"}
	}
	link, _ := fields["link"].(string)
	property, _ := fields["property"].(string)
	if link == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names no link"}
	}
	if property == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names link %q and no property on it", link)}
	}
	return outputRef{Link: link, Property: property}, true, nil
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
