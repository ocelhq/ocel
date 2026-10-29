package pulumi

import (
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

const (
	unknownSentinel = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"
	maskedSecret    = "[secret]"
	signatureKey    = "4dabf18193072939515e22adb298388d"
	secretSignature = "1b47061264138c4ac30d75fd1eb44270"
	outputSignature = "d0e6a833031e9bbcd3f4e8bde6ca49a4"
)

func readProperties(inputs, outputs map[string]any, names fieldNames) properties.Properties {
	var unknown []string
	merged, _ := merge(inputs, outputs).(map[string]any)
	values, _ := convert(merged, "", names, false, &unknown)
	held, _ := values.(map[string]any)
	return properties.New(held, unknown)
}

func merge(input, output any) any {
	if output == nil {
		return input
	}
	if input == nil {
		return dropUnknowns(output)
	}
	if output == unknownSentinel {
		return input
	}
	if in, inList := input.([]any); inList {
		if out, outList := output.([]any); outList && len(in) == len(out) {
			merged := make([]any, len(out))
			for i, value := range out {
				merged[i] = merge(in[i], value)
			}
			return merged
		}
	}
	in, inMap := input.(map[string]any)
	out, outMap := output.(map[string]any)
	if !inMap || !outMap || isSigned(in) || isSigned(out) {
		return output
	}
	merged := make(map[string]any, len(in)+len(out))
	for key, value := range in {
		merged[key] = value
	}
	for key, value := range out {
		merged[key] = merge(in[key], value)
	}
	return merged
}

func dropUnknowns(output any) any {
	switch held := output.(type) {
	case string:
		if held == unknownSentinel {
			return nil
		}
	case map[string]any:
		if isSigned(held) {
			return held
		}
		out := make(map[string]any, len(held))
		for key, value := range held {
			if known := dropUnknowns(value); known != nil {
				out[key] = known
			}
		}
		return out
	case []any:
		out := make([]any, len(held))
		for i, value := range held {
			if out[i] = dropUnknowns(value); out[i] == nil && value != nil {
				return nil
			}
		}
		return out
	}
	return output
}

func isSigned(value map[string]any) bool {
	_, signed := value[signatureKey]
	return signed
}

func convert(value any, path string, names fieldNames, verbatim bool, unknown *[]string) (any, bool) {
	switch held := value.(type) {
	case nil:
		return nil, false
	case string:
		if held == unknownSentinel || held == maskedSecret {
			*unknown = append(*unknown, path)
			return nil, false
		}
		return held, true
	case map[string]any:
		if isSigned(held) {
			return convertSigned(held, path, names, unknown)
		}
		out := make(map[string]any, len(held))
		for key, nested := range held {
			if strings.HasPrefix(key, "__") {
				continue
			}
			name, nestedNames := key, fieldNames(nil)
			if !verbatim {
				name, nestedNames = names.terraformName(key)
			}
			if carried, keep := convert(nested, properties.Join(path, name), nestedNames, !verbatim && properties.IsMapValued(name), unknown); keep {
				out[name] = carried
			}
		}
		return out, true
	case []any:
		if object, ok := properties.OnlyObject(held); ok && !isSigned(object) {
			return convert(object, path, names, false, unknown)
		}
		mark := len(*unknown)
		out := make([]any, 0, len(held))
		for i, nested := range held {
			if nested == nil {
				out = append(out, nil)
				continue
			}
			carried, keep := convert(nested, properties.Join(path, strconv.Itoa(i)), names, false, unknown)
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

func convertSigned(value map[string]any, path string, names fieldNames, unknown *[]string) (any, bool) {
	switch value[signatureKey] {
	case secretSignature:
		*unknown = append(*unknown, path)
		return nil, false
	case outputSignature:
		if secret, _ := value["secret"].(bool); secret {
			*unknown = append(*unknown, path)
			return nil, false
		}
		if known, present := value["value"]; present {
			return convert(known, path, names, false, unknown)
		}
		*unknown = append(*unknown, path)
		return nil, false
	}
	return nil, false
}
