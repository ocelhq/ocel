package configdoc

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
)

type Bindings map[string]map[string]string

func BindableTypes() []string {
	out := make([]string, 0, 4)
	for _, typ := range naming.BindableResourceTypes() {
		out = append(out, naming.ResourceTypeName(typ))
	}
	slices.Sort(out)
	return out
}

func (Bindings) checkShape(path string, value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return typeError(path, "an object")
	}
	known := BindableTypes()
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if !slices.Contains(known, key) {
			return unknownKeyError(path, key, known)
		}
		named, ok := object[key].(map[string]any)
		if !ok {
			return typeError(joinPath(path, key), "an object of declared name to published name")
		}
		for _, declared := range slices.Sorted(mapKeys(named)) {
			if _, spelled := named[declared].(string); !spelled {
				return typeError(joinPath(joinPath(path, key), declared), "the name the record is published under")
			}
		}
	}
	return nil
}

func (Bindings) jsonSchema() object {
	properties := object{}
	for _, name := range BindableTypes() {
		properties[name] = object{
			"type":                 "object",
			"additionalProperties": object{"type": "string"},
			"description": fmt.Sprintf(
				"Each key is a %s resource this project declares; its value is the name the record is published under.",
				name),
		}
	}
	return object{"type": "object", "properties": properties, "additionalProperties": false}
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}
