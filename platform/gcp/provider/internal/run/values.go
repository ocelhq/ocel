package run

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func MergeValues(what, source string, set, added map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(set)+len(added))
	maps.Copy(values, set)
	for _, name := range slices.Sorted(maps.Keys(added)) {
		if _, taken := values[name]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s takes %s from %s, and the deploy already set a value for %s: "+
					"a revision has one entry per name, so one value would silently take the place of the other "+
					"and the app would read a value nothing in it declared. Rename one of them",
				what, name, source, name)
		}
		values[name] = added[name]
	}
	return values, nil
}
