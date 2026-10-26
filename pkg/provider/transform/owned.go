package transform

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseOwned(site string, patch map[string]any, owned []string) error {
	for _, field := range owned {
		if sets(patch, strings.Split(field, ".")) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform sets %s.%s, which ocel fills from what this deploy built: drop it and let ocel own it",
				site, field)
		}
	}
	return nil
}

func sets(patch map[string]any, path []string) bool {
	value, present := patch[path[0]]
	if !present {
		return false
	}
	if len(path) == 1 {
		return true
	}
	nested, isObject := value.(map[string]any)
	if !isObject || IsOutput(nested) {
		return true
	}
	return sets(nested, path[1:])
}
