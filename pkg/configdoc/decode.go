package configdoc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type Lookup func(name string) (string, bool)

func Decode(data []byte, lookup Lookup) (*Document, error) {
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, typeError("", "an object")
	}

	interpolated, err := interpolate("", tree, lookup)
	if err != nil {
		return nil, err
	}
	if err := checkValue("", reflect.TypeOf(Document{}), interpolated); err != nil {
		return nil, err
	}

	normalized, err := json.Marshal(interpolated)
	if err != nil {
		return nil, err
	}
	doc := &Document{}
	if err := json.Unmarshal(normalized, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func interpolate(path string, value any, lookup Lookup) (any, error) {
	switch shaped := value.(type) {
	case string:
		expanded, err := expand(shaped, lookup)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", path, err)
		}
		return expanded, nil
	case []any:
		out := make([]any, len(shaped))
		for i, item := range shaped {
			expanded, err := interpolate(fmt.Sprintf("%s[%d]", path, i), item, lookup)
			if err != nil {
				return nil, err
			}
			out[i] = expanded
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(shaped))
		for key, item := range shaped {
			expanded, err := interpolate(joinPath(path, key), item, lookup)
			if err != nil {
				return nil, err
			}
			out[key] = expanded
		}
		return out, nil
	default:
		return value, nil
	}
}

func expand(value string, lookup Lookup) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '$' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 < len(value) && value[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if i+1 >= len(value) || value[i+1] != '{' {
			out.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("%q opens ${ and never closes it — write $$ where a literal dollar belongs", value)
		}
		name := value[i+2 : i+2+end]
		if name == "" {
			return "", fmt.Errorf("%q interpolates a variable with no name", value)
		}
		resolved, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("%s is not set in the environment or in .env, and this config reads it — set it, or write the value out", name)
		}
		out.WriteString(resolved)
		i += 2 + end + 1
	}
	return out.String(), nil
}
