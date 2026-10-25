package project

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
)

type BoundVariable struct {
	Key      string
	Binding  string
	Property string
}

func (v BoundVariable) Spelled() string {
	return "@" + v.Binding + "." + v.Property
}

func normalizeBoundVariables(raw map[string]configdoc.Binding) ([]BoundVariable, error) {
	out := make([]BoundVariable, 0, len(raw))
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		path := "bindings." + configdoc.VariablesKey + "." + key
		if !variableKey.MatchString(key) {
			return nil, fmt.Errorf("`%s` binds the variable %q, and a variable is named in upper-case letters, digits and underscores, not starting with a digit", path, key)
		}
		if strings.HasPrefix(key, ocelPrefix) {
			return nil, fmt.Errorf("`%s` binds the variable %q, and names starting %s are the ones ocel writes itself — name it something else", path, key, ocelPrefix)
		}
		spelled := strings.TrimSpace(raw[key].Published)
		if !strings.HasPrefix(spelled, "@") {
			return nil, fmt.Errorf("`%s` is %q; a variable takes a published record's property, written %q", path, spelled, "@"+spelled)
		}
		at := strings.LastIndex(spelled, ".")
		if at < 0 || at == len(spelled)-1 {
			return nil, fmt.Errorf("`%s` is %q, which names no property: write the record's name, a dot and the property the variable takes, as \"@<name>.<property>\"", path, spelled)
		}
		binding, err := publishedName(path, spelled[:at])
		if err != nil {
			return nil, err
		}
		out = append(out, BoundVariable{Key: key, Binding: binding, Property: spelled[at+1:]})
	}
	return out, nil
}
