package surface

import (
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider/transform"
)

const (
	DatabasePlacement = "settings.ipConfiguration.pscConfigs"
	StorePlacement    = "desiredAutoCreatedEndpoints"
	EgressPlacement   = "template.vpcAccess"
)

func ReadPlacement(result transform.Result, path string) ([]string, bool) {
	var named []string
	set := false
	for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
		for _, reached := range reach(result.Patches[key], strings.Split(path, ".")) {
			set = true
			named = append(named, leafStrings(reached)...)
		}
	}
	return named, set
}

func reach(value any, keys []string) []any {
	if value == nil {
		return nil
	}
	if len(keys) == 0 {
		return []any{value}
	}
	switch typed := value.(type) {
	case []any:
		var reached []any
		for _, item := range typed {
			reached = append(reached, reach(item, keys)...)
		}
		return reached
	case map[string]any:
		if transform.IsOutput(typed) {
			return []any{typed}
		}
		return reach(typed[keys[0]], keys[1:])
	}
	return nil
}

func leafStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var named []string
		for _, item := range typed {
			named = append(named, leafStrings(item)...)
		}
		return named
	case map[string]any:
		if transform.IsOutput(typed) {
			return []string{""}
		}
		var named []string
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			named = append(named, leafStrings(typed[key])...)
		}
		return named
	}
	return nil
}

func (a Allowed) HasPlacement(path string) bool {
	for _, paths := range a {
		if slices.ContainsFunc(paths, func(allowed string) bool { return allowed == path || strings.HasPrefix(allowed, path+".") }) {
			return true
		}
	}
	return false
}
