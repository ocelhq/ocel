package configdoc

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

const SchemaDialect = "https://json-schema.org/draft/2020-12/schema"

type AlsoAString interface {
	AlsoAString() bool
}

type object = map[string]any

func Schema() ([]byte, error) {
	root := schemaOf(reflect.TypeOf(Document{}))
	root["$schema"] = SchemaDialect
	root["title"] = "Ocel project configuration"
	return json.MarshalIndent(root, "", "  ")
}

func OptionsSchema(name string, options any) ([]byte, error) {
	shape := schemaOf(reflect.TypeOf(options))
	shape["title"] = strings.ToUpper(name[:1]) + name[1:] + "ProviderOptions"
	variant := object{
		"type": "object",
		"properties": object{
			"name":    object{"const": name},
			"options": shape,
		},
		"required":             []any{"name"},
		"additionalProperties": false,
	}
	return json.MarshalIndent(variant, "", "  ")
}

func schemaOf(target reflect.Type) object {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == rawMessageType {
		return object{"type": "object"}
	}

	value := reflect.New(target).Elem().Interface()
	switch shaped := value.(type) {
	case StringList:
		return object{"oneOf": []any{
			object{"type": "string"},
			object{"type": "array", "items": object{"type": "string"}},
		}}
	case Runtime:
		return object{"oneOf": []any{
			object{"type": "string", "enum": enumOf(RuntimeObject{}, "name")},
			schemaOf(reflect.TypeOf(RuntimeObject{})),
		}}
	case AlsoAString:
		if shaped.AlsoAString() {
			return object{"oneOf": []any{object{"type": "string"}, objectSchema(target)}}
		}
	}

	switch target.Kind() {
	case reflect.Struct:
		return objectSchema(target)
	case reflect.Slice, reflect.Array:
		return object{"type": "array", "items": schemaOf(target.Elem())}
	case reflect.Map:
		return object{"type": "object", "additionalProperties": schemaOf(target.Elem())}
	case reflect.String:
		return object{"type": "string"}
	case reflect.Bool:
		return object{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return object{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return object{"type": "number"}
	default:
		return object{}
	}
}

func objectSchema(target reflect.Type) object {
	properties := object{}
	var required []any
	for _, field := range jsonFields(target) {
		property := schemaOf(field.kind)
		if field.doc != "" {
			property["description"] = field.doc
		}
		if len(field.enum) > 0 {
			if items, ok := property["items"].(object); ok {
				items["enum"] = toAny(field.enum)
			} else {
				property["enum"] = toAny(field.enum)
			}
		}
		properties[field.name] = property
		if !field.optional {
			required = append(required, field.name)
		}
	}
	schema := object{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	if name := typeName(target); name != "" {
		schema["title"] = name
	}
	return schema
}

func typeName(target reflect.Type) string {
	name := target.Name()
	if name == "" || strings.HasPrefix(name, "struct {") {
		return ""
	}
	return name
}

func enumOf(target any, name string) []any {
	for _, field := range jsonFields(reflect.TypeOf(target)) {
		if field.name == name {
			return toAny(field.enum)
		}
	}
	return nil
}

func toAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range slices.Clone(values) {
		out = append(out, value)
	}
	return out
}
