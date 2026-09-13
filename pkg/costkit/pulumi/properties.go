package pulumi

import (
	"slices"
	"strconv"
)

const (
	unknownSentinel = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"
	secretSignature = "4dabf18193072939515e22adb298388d"
)

func properties(inputs map[string]any) (map[string]any, []string) {
	var unknown []string
	converted, _ := convert(inputs, "", &unknown)
	held, _ := converted.(map[string]any)
	slices.Sort(unknown)
	return held, unknown
}

func convert(value any, path string, unknown *[]string) (any, bool) {
	switch held := value.(type) {
	case string:
		if held == unknownSentinel {
			*unknown = append(*unknown, path)
			return nil, false
		}
		return held, true
	case map[string]any:
		if _, secret := held[secretSignature]; secret {
			*unknown = append(*unknown, path)
			return nil, false
		}
		out := make(map[string]any, len(held))
		for key, nested := range held {
			name := snake(key)
			if carried, keep := convert(nested, join(path, name), unknown); keep {
				out[name] = carried
			}
		}
		return out, true
	case []any:
		mark := len(*unknown)
		out := make([]any, 0, len(held))
		for i, nested := range held {
			carried, keep := convert(nested, join(path, strconv.Itoa(i)), unknown)
			if !keep {
				*unknown = append((*unknown)[:mark], path)
				return nil, false
			}
			out = append(out, carried)
		}
		return out, true
	default:
		return held, true
	}
}

func join(path, segment string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}

func tags(inputs map[string]any) map[string]string {
	held, ok := inputs["tags"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(held))
	for key, value := range held {
		text, ok := value.(string)
		if !ok {
			return nil
		}
		out[key] = text
	}
	return out
}
