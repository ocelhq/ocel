package configdoc

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type Keyed interface {
	Shorthands() []string
}

func keyedSchema(target reflect.Type, shorthands []string) object {
	alternatives := []any{object{"type": "string", "enum": toAny(shorthands)}}
	for _, field := range jsonFields(target) {
		property := schemaOf(field.kind)
		if field.doc != "" {
			property["description"] = field.doc
		}
		alternatives = append(alternatives, object{
			"type":                 "object",
			"properties":           object{field.name: property},
			"required":             []any{field.name},
			"additionalProperties": false,
		})
	}
	schema := object{"oneOf": alternatives}
	if name := typeName(target); name != "" {
		schema["title"] = name
	}
	return schema
}

func checkKeyed(path string, target reflect.Type, shorthands []string, value any) error {
	fields := jsonFields(target)
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		keys = append(keys, field.name)
	}
	switch spelled := value.(type) {
	case string:
		if slices.Contains(shorthands, spelled) {
			return nil
		}
	case map[string]any:
		if len(spelled) != 1 {
			return fmt.Errorf("%s holds exactly one of the keys %s", PathName(path), strings.Join(keys, ", "))
		}
		for key, held := range spelled {
			at := slices.IndexFunc(fields, func(field jsonField) bool { return field.name == key })
			if at < 0 {
				return unknownKeyError(path, key, keys)
			}
			if held == nil {
				return nil
			}
			return checkValue(JoinPath(path, key), fields[at].kind, held)
		}
	}
	quoted := make([]string, 0, len(shorthands))
	for _, shorthand := range shorthands {
		quoted = append(quoted, strconv.Quote(shorthand))
	}
	return fmt.Errorf("%s must be one of %s, or an object holding one of the keys %s",
		PathName(path), strings.Join(quoted, ", "), strings.Join(keys, ", "))
}
