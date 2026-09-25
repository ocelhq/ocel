package configdoc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
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

	interpolated, err := interpolate("", reflect.TypeOf(Document{}), tree, lookup)
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

func interpolate(path string, target reflect.Type, value any, lookup Lookup) (any, error) {
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
			expanded, err := interpolate(IndexPath(path, i), elementOf(target), item, lookup)
			if err != nil {
				return nil, err
			}
			out[i] = expanded
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(shaped))
		for key, item := range shaped {
			field, secret := memberOf(target, key)
			if text, spelled := item.(string); spelled && secret != "" {
				if err := checkSecret(JoinPath(path, key), secret, text); err != nil {
					return nil, err
				}
				out[key] = text
				continue
			}
			expanded, err := interpolate(JoinPath(path, key), field, item, lookup)
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

func typed(target reflect.Type) reflect.Type {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == nil || target == rawMessageType {
		return nil
	}
	zero := reflect.New(target).Elem().Interface()
	if _, checked := zero.(shapeChecker); checked {
		return nil
	}
	if _, alsoText := zero.(AlsoAString); alsoText {
		return nil
	}
	return target
}

func elementOf(target reflect.Type) reflect.Type {
	target = typed(target)
	if target == nil || (target.Kind() != reflect.Slice && target.Kind() != reflect.Array) {
		return nil
	}
	return target.Elem()
}

func memberOf(target reflect.Type, key string) (reflect.Type, string) {
	target = typed(target)
	if target == nil {
		return nil, ""
	}
	switch target.Kind() {
	case reflect.Map:
		return target.Elem(), ""
	case reflect.Struct:
		for _, field := range jsonFields(target) {
			if field.name == key {
				return field.kind, field.secret
			}
		}
	}
	return nil, ""
}

var secretPlaceholder = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

var variableName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func SecretVariable(placeholder string) (string, bool) {
	match := secretPlaceholder.FindStringSubmatch(placeholder)
	if match == nil {
		return "", false
	}
	return match[1], true
}

func checkSecret(path, example, value string) error {
	if _, ok := SecretVariable(value); ok {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	switch {
	case variableName.MatchString(trimmed):
		return fmt.Errorf("%s is a secret, so the config holds where it comes from rather than the name alone: write it as %q", PathName(path), "${"+trimmed+"}")
	case strings.Contains(value, "${"):
		return fmt.Errorf("%s is a secret, and a secret is one placeholder as its whole value, such as %q, with nothing around it", PathName(path), "${"+example+"}")
	default:
		return fmt.Errorf("%s is a secret, and the config never holds one: write %q and export the secret under that name. In ocel.config.ts a value read with buildEnv lands here as the secret itself, so write the placeholder string there too", PathName(path), "${"+example+"}")
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
		if strings.HasPrefix(value[i:], "$${") {
			out.WriteString("${")
			i += 3
			continue
		}
		if i+1 >= len(value) || value[i+1] != '{' {
			out.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("%q opens ${ and never closes it — write $${ where a literal ${ belongs", value)
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
