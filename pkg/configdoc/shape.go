package configdoc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type shapeChecker interface {
	checkShape(path string, value any) error
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

func typeError(path, want string) error {
	if path == "" {
		return fmt.Errorf("the config must be an object")
	}
	return fmt.Errorf("%q must be %s", path, want)
}

type UnknownKeyError struct {
	Path  string
	Known []string
}

func (e UnknownKeyError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("%q is not a key this config has", e.Path)
	}
	return fmt.Sprintf("%q is not a key this config has — the keys here are %s", e.Path, strings.Join(e.Known, ", "))
}

func unknownKeyError(path, key string, known []string) error {
	at := key
	if path != "" {
		at = path + "." + key
	}
	return UnknownKeyError{Path: at, Known: known}
}

func Check(path string, target any, value any) error {
	return checkValue(path, reflect.TypeOf(target), value)
}

func checkStruct(path string, target any, value map[string]any) error {
	return checkValue(path, reflect.TypeOf(target), value)
}

func checkValue(path string, target reflect.Type, value any) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == rawMessageType {
		return nil
	}
	zero := reflect.New(target).Elem().Interface()
	if checker, ok := zero.(shapeChecker); ok {
		return checker.checkShape(path, value)
	}
	if _, ok := zero.(AlsoAString); ok {
		if _, spelled := value.(string); spelled {
			return nil
		}
	}

	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return typeError(path, "an object")
		}
		return checkObject(path, target, object)
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return typeError(path, "a list")
		}
		for i, item := range items {
			if err := checkValue(fmt.Sprintf("%s[%d]", path, i), target.Elem(), item); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return typeError(path, "an object")
		}
		for key, item := range object {
			if err := checkValue(joinPath(path, key), target.Elem(), item); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		if _, ok := value.(string); !ok {
			return typeError(path, "text")
		}
		return nil
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return typeError(path, "true or false")
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if _, ok := value.(float64); !ok {
			return typeError(path, "a number")
		}
		return nil
	default:
		return nil
	}
}

func checkObject(path string, target reflect.Type, object map[string]any) error {
	fields := jsonFields(target)
	known := make([]string, 0, len(fields))
	for _, field := range fields {
		known = append(known, field.name)
	}

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		index := slices.IndexFunc(fields, func(f jsonField) bool { return f.name == key })
		if index < 0 {
			return unknownKeyError(path, key, known)
		}
		if object[key] == nil {
			continue
		}
		if err := checkValue(joinPath(path, key), fields[index].kind, object[key]); err != nil {
			return err
		}
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

type jsonField struct {
	name     string
	doc      string
	enum     []string
	optional bool
	kind     reflect.Type
}

func jsonFields(target reflect.Type) []jsonField {
	fields := make([]jsonField, 0, target.NumField())
	for i := range target.NumField() {
		field := target.Field(i)
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		var enum []string
		if raw := field.Tag.Get("enum"); raw != "" {
			enum = strings.Split(raw, ",")
		}
		fields = append(fields, jsonField{
			name:     name,
			doc:      field.Tag.Get("doc"),
			enum:     enum,
			optional: strings.Contains(options, "omitempty"),
			kind:     field.Type,
		})
	}
	return fields
}
