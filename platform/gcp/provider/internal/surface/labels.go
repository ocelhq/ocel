package surface

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	ownLabelPrefix  = "ocel-"
	labelsPerObject = 64
)

var (
	labelKey   = regexp.MustCompile(`^[\p{Ll}\p{Lo}][\p{Ll}\p{Lo}\p{N}_-]{0,62}$`)
	labelValue = regexp.MustCompile(`^[\p{Ll}\p{Lo}\p{N}_-]{0,63}$`)
)

func RefuseInvalidLabels(resource string, tags map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		if strings.HasPrefix(key, ownLabelPrefix) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform tags %s with %s, and the %s prefix is ocel's own: rename the tag", resource, key, ownLabelPrefix)
		}
		if !labelKey.MatchString(key) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform tags %s with %q, which Google Cloud refuses as a label key: start it with a lowercase letter and keep to at most 63 lowercase letters, digits, - and _", resource, key)
		}
		if !labelValue.MatchString(tags[key]) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform tags %s with %s=%q, which Google Cloud refuses as a label value: keep it to at most 63 lowercase letters, digits, - and _", resource, key, tags[key])
		}
	}
	return nil
}

func refuseTooManyLabels(resource string, labels map[string]string) error {
	if len(labels) <= labelsPerObject {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform's tags give %s %d labels, and Google Cloud holds at most %d on one resource: drop some", resource, len(labels), labelsPerObject)
}
