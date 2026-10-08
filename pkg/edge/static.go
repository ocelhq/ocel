package edge

import (
	"encoding/json"
	"slices"
	"strings"
)

const (
	ImmutableCacheControl  = "public, max-age=31536000, immutable"
	RevalidateCacheControl = "public, max-age=0, must-revalidate"
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

func (s *Static) CacheControl(path string) string {
	if s.IsImmutable(path) {
		return ImmutableCacheControl
	}
	return RevalidateCacheControl
}

func (s *Static) Variable() string {
	if s == nil {
		return ""
	}
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
