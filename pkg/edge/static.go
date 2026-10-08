package edge

import (
	"slices"
	"strings"
)

type Static struct {
	ImmutablePrefixes      []string `json:"immutablePrefixes"`
	MustRevalidatePrefixes []string `json:"mustRevalidatePrefixes,omitempty"`
}

func (s *Static) IsImmutable(path string) bool {
	if s == nil {
		return false
	}
	under := func(prefix string) bool { return strings.HasPrefix(path, prefix) }
	return slices.ContainsFunc(s.ImmutablePrefixes, under) && !slices.ContainsFunc(s.MustRevalidatePrefixes, under)
}
