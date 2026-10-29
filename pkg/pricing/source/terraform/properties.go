package terraform

import (
	"strconv"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

func readProperties(values, unknownMarks, sensitiveMarks any, config configured) properties.Properties {
	var unknown []string
	converted, _ := convert(values, unknownMarks, sensitiveMarks, config, "", &unknown)
	held, _ := converted.(map[string]any)
	return properties.New(held, unknown)
}

func convert(value, unknownMark, sensitiveMark any, config configured, path string, unknown *[]string) (any, bool) {
	if unknownMark == true && config.isUnconfigured() {
		return nil, false
	}
	if unknownMark == true || (sensitiveMark == true && value != nil) {
		*unknown = append(*unknown, path)
		return nil, false
	}
	if sensitiveMark == true {
		return nil, false
	}
	switch held := value.(type) {
	case nil:
		if object, ok := unknownMark.(map[string]any); ok {
			return convertObject(nil, object, sensitiveMark, config, path, unknown)
		}
		return nil, false
	case map[string]any:
		return convertObject(held, unknownMark, sensitiveMark, config, path, unknown)
	case []any:
		if object, ok := properties.OnlyObject(held); ok && at(unknownMark, 0) != true && at(sensitiveMark, 0) != true {
			return convertObject(object, at(unknownMark, 0), at(sensitiveMark, 0), config.element(1), path, unknown)
		}
		mark := len(*unknown)
		out := make([]any, 0, len(held))
		for i, nested := range held {
			if at(unknownMark, i) == true && config.isUnconfigured() {
				*unknown = (*unknown)[:mark]
				return nil, false
			}
			if at(unknownMark, i) == true || at(sensitiveMark, i) == true {
				*unknown = append((*unknown)[:mark], path)
				return nil, false
			}
			carried, _ := convert(nested, at(unknownMark, i), at(sensitiveMark, i), config.element(len(held)), properties.Join(path, strconv.Itoa(i)), unknown)
			out = append(out, carried)
		}
		return out, true
	default:
		return held, true
	}
}

func convertObject(object map[string]any, unknownMark, sensitiveMark any, config configured, path string, unknown *[]string) (any, bool) {
	unknownFields, _ := unknownMark.(map[string]any)
	sensitiveFields, _ := sensitiveMark.(map[string]any)
	out := make(map[string]any, len(object))
	for key, nested := range object {
		if carried, keep := convert(nested, unknownFields[key], sensitiveFields[key], config.field(key, nested), properties.Join(path, key), unknown); keep {
			out[key] = carried
		}
	}
	for key, mark := range unknownFields {
		if _, present := object[key]; !present && mark == true && !config.field(key, nil).isUnconfigured() {
			*unknown = append(*unknown, properties.Join(path, key))
		}
	}
	if object == nil {
		return nil, false
	}
	return out, true
}

func at(mark any, i int) any {
	list, ok := mark.([]any)
	if !ok || i >= len(list) {
		return nil
	}
	return list[i]
}
